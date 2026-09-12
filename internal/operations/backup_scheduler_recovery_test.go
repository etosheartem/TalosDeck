package operations

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"talosdeck/internal/clusters"
	"talosdeck/internal/jobs"
)

type failScheduleWrite struct{ ProvisionStore }

func (s failScheduleWrite) PutSecret(ctx context.Context, scope, kind, key string, data []byte) error {
	if kind == "backup-schedule" {
		return errors.New("simulated registry write failure")
	}
	return s.ProvisionStore.PutSecret(ctx, scope, kind, key, data)
}

func TestSchedulerRecoversJournalRegistryCrashWithoutDuplicateExecution(t *testing.T) {
	dir := t.TempDir()
	db, key := filepath.Join(dir, "registry.db"), filepath.Join(dir, "key")
	store, err := clusters.Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cluster, err := store.Create(ctx, clusters.Cluster{Name: "scheduler", Identity: "scheduler"}, clusters.Credentials{Talosconfig: []byte("fixture-only")})
	if err != nil {
		t.Fatal(err)
	}
	s := &BackupService{ClusterID: cluster.ID, Store: store}
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	q := BackupSchedule{Enabled: true, IntervalHours: 1, Retention: 2, TargetID: "local", NextRun: due}
	if err = s.put(ctx, "backup-schedule", "schedule", q); err != nil {
		t.Fatal(err)
	}
	var executions atomic.Int32
	started := make(chan struct{}, 1)
	runner := func(context.Context, *jobs.Execution, jobs.Request) error {
		executions.Add(1)
		started <- struct{}{}
		return nil
	}
	journal := filepath.Join(dir, "jobs")
	jm, err := jobs.OpenCluster(journal, cluster.ID, runner)
	if err != nil {
		t.Fatal(err)
	}
	s.Store = failScheduleWrite{store}
	if err = s.scheduleDue(ctx, jm, now); err == nil {
		t.Fatal("failed schedule persistence hidden")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("durable scheduled job did not run")
	}
	jm.Close()
	store.Close()
	store, err = clusters.Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = &BackupService{ClusterID: cluster.ID, Store: store}
	got, err := s.Schedule(ctx)
	if err != nil || !got.Enabled || got.Retention != 2 || !got.NextRun.Equal(due) {
		t.Fatalf("schedule was not recovered: %+v %v", got, err)
	}
	jm, err = jobs.OpenCluster(journal, cluster.ID, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer jm.Close()
	if err = s.scheduleDue(ctx, jm, now); err != nil {
		t.Fatal(err)
	}
	if executions.Load() != 1 || len(jm.List()) != 1 {
		t.Fatal("crash window replayed scheduled backup")
	}
	got, err = s.Schedule(ctx)
	if err != nil || !got.NextRun.Equal(now.Add(time.Hour)) {
		t.Fatalf("due time not advanced after durable deduplication: %+v %v", got, err)
	}
}

func TestSchedulerBusyJournalPreservesDueTime(t *testing.T) {
	s := backupFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	q := BackupSchedule{Enabled: true, IntervalHours: 1, Retention: 2, TargetID: "local", NextRun: now.Add(-time.Minute)}
	if err := s.put(ctx, "backup-schedule", "schedule", q); err != nil {
		t.Fatal(err)
	}
	jm, err := jobs.Open(t.TempDir(), func(context.Context, *jobs.Execution, jobs.Request) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer jm.Close()
	release, err := jm.ReserveManual()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err = s.scheduleDue(ctx, jm, now); !errors.Is(err, jobs.ErrBusy) {
		t.Fatalf("busy journal: %v", err)
	}
	got, err := s.Schedule(ctx)
	if err != nil || !got.NextRun.Equal(q.NextRun) || len(jm.List()) != 0 {
		t.Fatalf("busy scheduler skipped due backup: %+v %v", got, err)
	}
}

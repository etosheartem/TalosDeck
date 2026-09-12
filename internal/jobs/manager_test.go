package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClusterJournalIdentityAndLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	m, err := Open(dir, func(context.Context, *Execution, Request) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.Submit(Request{Kind: "test"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, m, j.ID, "succeeded")
	m.Close()
	bound, err := OpenCluster(dir, "cluster-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	j, err = bound.Get(j.ID)
	if err != nil || j.ClusterID != "cluster-a" {
		t.Fatalf("%+v %v", j, err)
	}
	bound.Close()
	if foreign, err := OpenCluster(dir, "cluster-b", nil); err == nil {
		foreign.Close()
		t.Fatal("foreign cluster accepted journal")
	}
}

func TestAmbiguousRunnerOutcomesRequireReview(t *testing.T) {
	for _, outcome := range []string{"panic", "timeout", "uncertain"} {
		t.Run(outcome, func(t *testing.T) {
			m, err := Open(t.TempDir(), func(context.Context, *Execution, Request) error {
				switch outcome {
				case "panic":
					panic("private-key-never-log-this")
				case "timeout":
					return context.DeadlineExceeded
				default:
					return ErrUncertain
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			j, err := m.Submit(Request{Kind: "test"}, "admin")
			if err != nil {
				t.Fatal(err)
			}
			done := waitJob(t, m, j.ID, "interrupted")
			if strings.Contains(done.Error, "private-key") {
				t.Fatal("panic leaked private data")
			}
			if _, err := m.ReserveManual(); !errors.Is(err, ErrBusy) {
				t.Fatal("manual operation allowed before review")
			}
			if err := m.Acknowledge(j.ID, "reviewer"); err != nil {
				t.Fatal(err)
			}
			release, err := m.ReserveManual()
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}

func TestJournalRedactsSensitiveRunnerOutputAndErrors(t *testing.T) {
	m, err := Open(t.TempDir(), func(_ context.Context, e *Execution, _ Request) error {
		if err := e.Log("command", "authorization: Bearer top-private-value"); err != nil {
			return err
		}
		return errors.New("password=top-private-value")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, err := m.Submit(Request{Kind: "test"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	done := waitJob(t, m, j.ID, "failed")
	data, err := os.ReadFile(filepath.Join(m.dir, j.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "top-private-value") || !strings.Contains(done.Error, "redacted") {
		t.Fatal("sensitive output persisted")
	}
}

func waitJob(t *testing.T, m *Manager, id string, status string) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, err := m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status == status {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	j, _ := m.Get(id)
	t.Fatalf("expected %s, got %+v", status, j)
	return Job{}
}
func TestDurableExecutionAndExclusiveSubmission(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{})
	finish := make(chan struct{})
	m, err := Open(dir, func(ctx context.Context, e *Execution, _ Request) error {
		if err := e.Checkpoint(ctx, "node-1", "about to change node"); err != nil {
			return err
		}
		close(started)
		<-finish
		return e.Log("verified", "node is ready")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, err := m.Submit(Request{Kind: "test"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err = m.Submit(Request{}, "other"); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent submit: %v", err)
	}
	if _, err = m.ReserveManual(); !errors.Is(err, ErrBusy) {
		t.Fatalf("legacy mutation not blocked: %v", err)
	}
	if second, err := Open(dir, nil); err == nil {
		second.Close()
		t.Fatal("second writer opened same journal")
	}
	close(finish)
	done := waitJob(t, m, j.ID, "succeeded")
	if len(done.Events) < 3 {
		t.Fatal("missing execution history")
	}
	m.Close()
	restored, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	saved, err := restored.Get(j.ID)
	if err != nil || saved.Status != "succeeded" || len(saved.Events) != len(done.Events) {
		t.Fatalf("history not restored: %+v %v", saved, err)
	}
}
func TestRestartDoesNotReplayAndRequiresReview(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{})
	m, err := Open(dir, func(ctx context.Context, e *Execution, _ Request) error {
		if err := e.Checkpoint(ctx, "command", "issued"); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	j, err := m.Submit(Request{}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	m.Close()
	calls := 0
	m, err = Open(dir, func(context.Context, *Execution, Request) error { calls++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	saved, _ := m.Get(j.ID)
	if saved.Status != "interrupted" || calls != 0 {
		t.Fatalf("unexpected recovery: %+v, calls %d", saved, calls)
	}
	if _, err = m.Submit(Request{}, "admin"); !errors.Is(err, ErrBusy) {
		t.Fatalf("unreviewed job did not block: %v", err)
	}
	if err = m.Acknowledge(j.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	release, err := m.ReserveManual()
	if err != nil {
		t.Fatal(err)
	}
	release()
}
func TestStopFinishesCurrentStepAndSkipsNext(t *testing.T) {
	entered := make(chan struct{})
	finish := make(chan struct{})
	var reached bool
	m, err := Open(t.TempDir(), func(ctx context.Context, e *Execution, _ Request) error {
		if err := e.Checkpoint(ctx, "first", "start"); err != nil {
			return err
		}
		close(entered)
		<-finish
		if err := e.Checkpoint(ctx, "second", "start"); err != nil {
			return err
		}
		reached = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, _ := m.Submit(Request{}, "admin")
	<-entered
	if err = m.Stop(j.ID); err != nil {
		t.Fatal(err)
	}
	close(finish)
	waitJob(t, m, j.ID, "stopped")
	if reached {
		t.Fatal("stop started another side effect")
	}
}
func TestJournalFailurePreventsSideEffect(t *testing.T) {
	enter := make(chan struct{})
	resume := make(chan struct{})
	mutations := 0
	dir := t.TempDir()
	m, err := Open(dir, func(ctx context.Context, e *Execution, _ Request) error {
		close(enter)
		<-resume
		if err := e.Checkpoint(ctx, "node", "persist before mutation"); err != nil {
			return err
		}
		mutations++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, _ := m.Submit(Request{}, "admin")
	<-enter
	if err = os.Rename(dir, dir+"-moved"); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir + "-moved")
	close(resume)
	waitJob(t, m, j.ID, "interrupted")
	if mutations != 0 {
		t.Fatal("mutation executed without durable checkpoint")
	}
}
func TestManualReservationAndCorruptJournal(t *testing.T) {
	dir := t.TempDir()
	m, err := Open(dir, func(context.Context, *Execution, Request) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	release, _ := m.ReserveManual()
	if _, err = m.Submit(Request{}, "admin"); !errors.Is(err, ErrBusy) {
		t.Fatal("submit raced manual action")
	}
	release()
	m.Close()
	if err = os.WriteFile(filepath.Join(dir, "bad.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if m, err = Open(dir, nil); err == nil {
		m.Close()
		t.Fatal("corrupt journal silently discarded")
	}
}
func TestConcurrentSubmitOnlyOneWinner(t *testing.T) {
	m, err := Open(t.TempDir(), func(ctx context.Context, _ *Execution, _ Request) error { <-ctx.Done(); return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Submit(Request{}, "admin"); err == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("%d simultaneous mutations", winners)
	}
}

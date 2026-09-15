package recovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryKeepsObservationsSeparateAndSkipsCorruptRecords(t *testing.T) {
	dir := t.TempDir()
	created := time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)
	uploaded := created.Add(2 * time.Minute)
	if err := Append(dir, Record{Kind: "backup", Outcome: "succeeded", ObservedAt: uploaded, BackupCreatedAt: created, UploadedAt: &uploaded, ChecksumVerifiedAt: &uploaded}); err != nil {
		t.Fatal(err)
	}
	drilled := created.Add(6 * time.Hour)
	if err := Append(dir, Record{Kind: "drill", Outcome: "succeeded", ObservedAt: drilled, BackupCreatedAt: created, RestoreTestedAt: &drilled}); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, Record{Kind: "sabotage", Outcome: "succeeded"}); err == nil {
		t.Fatal("unknown record kind accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "20260101T000000.000000000Z-backup-corrupt.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	records, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("corrupt record hid evidence: %d", len(records))
	}
	if records[0].Kind != "drill" || !records[0].ObservedAt.Equal(drilled) {
		t.Fatal("history is not newest first", records[0])
	}
	// A drill proves a copy is restorable; it is not a new backup.
	if records[0].UploadedAt != nil || !records[0].BackupCreatedAt.Equal(created) {
		t.Fatal("drill restated the backup", records[0])
	}
	if records[1].Kind != "backup" || records[1].RestoreTestedAt != nil {
		t.Fatal("backup claimed a restore test", records[1])
	}
	if records[0].ID == records[1].ID || records[0].Version != 1 {
		t.Fatal("records are not independently identified")
	}
}

func TestHistoryIsBoundedAndDropsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < HistoryRecords+5; i++ {
		if err := Append(dir, Record{Kind: "drill", Outcome: "succeeded", ObservedAt: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != HistoryRecords {
		t.Fatalf("history unbounded: %d", len(records))
	}
	oldest := records[len(records)-1].ObservedAt
	if !oldest.Equal(base.Add(5 * time.Minute)) {
		t.Fatal("pruning did not drop the oldest observations first", oldest)
	}
}

func TestHistoryDisabledWithoutDirectory(t *testing.T) {
	if err := Append("", Record{Kind: "backup", Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	records, err := Load("")
	if err != nil || records != nil {
		t.Fatal(records, err)
	}
	records, err = Load(filepath.Join(t.TempDir(), "never-written"))
	if err != nil || len(records) != 0 {
		t.Fatal("missing history directory must read as empty, not as an error", records, err)
	}
}

// The whole history contract rests on one archive property: a drill re-reads the
// original manifest, so it can never present itself as a newer copy.
func TestDrillReportsTheOriginalCreationTime(t *testing.T) {
	o, s := fixture(t)
	s.Close()
	created, err := Create(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	drilled, err := Drill(context.Background(), o.ArchivePath, o.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !drilled.CreatedAt.Equal(created.CreatedAt) {
		t.Fatal("drill restated the creation time", created.CreatedAt, drilled.CreatedAt)
	}
	if drilled.SchemaVersion != created.SchemaVersion {
		t.Fatal("drill reported a different schema", drilled.SchemaVersion)
	}
	info, err := os.Stat(o.ArchivePath)
	if err != nil || info.Size() == 0 {
		t.Fatal("drill disturbed the source archive", info, err)
	}
}

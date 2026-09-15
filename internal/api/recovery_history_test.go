package api

import (
	"testing"
	"time"

	"talosdeck/internal/recovery"
)

func TestRecoverySummaryDoesNotLetVerificationReplaceABackup(t *testing.T) {
	created := time.Now().UTC().Add(-30 * time.Hour)
	uploaded := created.Add(time.Minute)
	drilled := time.Now().UTC().Add(-time.Hour)
	records := []recovery.Record{
		{Version: 1, Kind: "drill", Outcome: "succeeded", ObservedAt: drilled, BackupCreatedAt: created, ChecksumVerifiedAt: &drilled, RestoreTestedAt: &drilled},
		{Version: 1, Kind: "backup", Outcome: "succeeded", ObservedAt: uploaded, BackupCreatedAt: created, UploadedAt: &uploaded, ChecksumVerifiedAt: &uploaded},
	}
	summary := summarizeRecovery("/var/lib/talosdeck-recovery-history", records)
	if summary.LastBackupAt == nil || !summary.LastBackupAt.Equal(created) {
		t.Fatal("last backup is not the archive creation time", summary.LastBackupAt)
	}
	// The drill ran an hour ago but the newest recoverable copy is 30 hours old.
	if summary.RecoveryPointSeconds == nil || *summary.RecoveryPointSeconds < 29*3600 {
		t.Fatal("a restore drill shortened the recovery point", summary.RecoveryPointSeconds)
	}
	if summary.LastRestoreTestedAt == nil || !summary.LastRestoreTestedAt.Equal(drilled) {
		t.Fatal("restore-tested time missing", summary.LastRestoreTestedAt)
	}
	if summary.LastRestoreTestedFor == nil || !summary.LastRestoreTestedFor.Equal(created) {
		t.Fatal("restore-tested must name the backup it tested", summary.LastRestoreTestedFor)
	}
	if summary.UnresolvedObservation {
		t.Fatal("no ambiguous observation was recorded")
	}
}

func TestRecoverySummaryReportsUnprovenAndMissingEvidence(t *testing.T) {
	empty := summarizeRecovery("", nil)
	if empty.HistoryConfigured || empty.LastBackupAt != nil || empty.RecoveryPointSeconds != nil || empty.Records == nil {
		t.Fatal("unconfigured history must report no evidence, not a healthy state", empty)
	}
	now := time.Now().UTC()
	ambiguous := summarizeRecovery("dir", []recovery.Record{
		{Version: 1, Kind: "backup", Outcome: "unknown", ObservedAt: now, BackupCreatedAt: now, Error: "upload response lost"},
		{Version: 1, Kind: "drill", Outcome: "failed", ObservedAt: now, BackupCreatedAt: now},
	})
	if !ambiguous.UnresolvedObservation {
		t.Fatal("ambiguous upload not surfaced")
	}
	if ambiguous.LastBackupAt != nil || ambiguous.LastRestoreTestedAt != nil {
		t.Fatal("unproven outcomes counted as protection", ambiguous)
	}
}

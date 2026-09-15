package recovery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// HistoryRecords bounds what one directory may hold. Pruning history never
// prunes a backup: these files are observations, not recoverable copies.
const HistoryRecords = 500

const maxHistoryBytes = 64 << 10

// Record is one observation about a management-plane recovery archive. Times are
// recorded separately per check so a verification is never read as a new backup:
// BackupCreatedAt always comes from the archive manifest, never from this run's
// clock, and a drill leaves UploadedAt empty.
type Record struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	// Kind is backup, drill or restore. Outcome is succeeded, failed or unknown.
	Kind               string     `json:"kind"`
	Outcome            string     `json:"outcome"`
	ObservedAt         time.Time  `json:"observedAt"`
	BackupCreatedAt    time.Time  `json:"backupCreatedAt,omitempty"`
	UploadedAt         *time.Time `json:"uploadedAt,omitempty"`
	ChecksumVerifiedAt *time.Time `json:"checksumVerifiedAt,omitempty"`
	DecryptVerifiedAt  *time.Time `json:"decryptVerifiedAt,omitempty"`
	SchemaVerifiedAt   *time.Time `json:"schemaVerifiedAt,omitempty"`
	RestoreTestedAt    *time.Time `json:"restoreTestedAt,omitempty"`
	SchemaVersion      int        `json:"schemaVersion,omitempty"`
	ApplicationVersion string     `json:"applicationVersion,omitempty"`
	// Target locates the archive on the off-host target. Credentials never appear here.
	Target          string  `json:"target,omitempty"`
	SizeBytes       int64   `json:"sizeBytes,omitempty"`
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
	Error           string  `json:"error,omitempty"`
}

func validKind(kind string) bool {
	return kind == "backup" || kind == "drill" || kind == "restore"
}
func validOutcome(outcome string) bool {
	return outcome == "succeeded" || outcome == "failed" || outcome == "unknown"
}

// Append writes one immutable record. Existing records are never rewritten, so a
// later observation cannot silently restate an earlier one.
func Append(dir string, r Record) error {
	if dir == "" {
		return nil
	}
	if !validKind(r.Kind) || !validOutcome(r.Outcome) {
		return errors.New("invalid recovery history record")
	}
	r.Version = 1
	r.ID = uuid.NewString()
	if r.ObservedAt.IsZero() {
		r.ObservedAt = time.Now().UTC()
	}
	r.ObservedAt = r.ObservedAt.UTC()
	// A failed transfer must not be summarized as a proven property.
	if r.Outcome != "succeeded" {
		r.Error = truncate(r.Error, 4096)
	} else {
		r.Error = ""
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(raw) > maxHistoryBytes {
		return errors.New("recovery history record too large")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, r.ObservedAt.Format("20060102T150405.000000000Z")+"-"+r.Kind+"-"+r.ID+".json")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		os.Remove(path)
		return err
	}
	if closeErr != nil {
		os.Remove(path)
		return closeErr
	}
	// Records hold timestamps, versions and an archive locator, never secrets. An
	// explicit mode lets the reading service share this directory through a group
	// even when the writing unit runs with a restrictive umask.
	if err = os.Chmod(path, 0640); err != nil {
		os.Remove(path)
		return err
	}
	if err = syncDir(dir); err != nil {
		return err
	}
	prune(dir)
	return nil
}

// Load returns records newest first. Unreadable or invalid files are skipped:
// one corrupt observation must not hide the rest of the evidence.
func Load(dir string) ([]Record, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, e := entry.Info()
		if e != nil || info.Size() > maxHistoryBytes || !info.Mode().IsRegular() {
			continue
		}
		raw, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		if e != nil {
			continue
		}
		var r Record
		if json.Unmarshal(raw, &r) != nil || r.Version != 1 || !validKind(r.Kind) || !validOutcome(r.Outcome) || r.ObservedAt.IsZero() {
			continue
		}
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].ObservedAt.Equal(records[j].ObservedAt) {
			return records[i].ID > records[j].ID
		}
		return records[i].ObservedAt.After(records[j].ObservedAt)
	})
	if len(records) > HistoryRecords {
		records = records[:HistoryRecords]
	}
	return records, nil
}

func prune(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= HistoryRecords {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	if len(names) <= HistoryRecords {
		return
	}
	// Names start with the observation timestamp, so lexical order is chronological.
	sort.Strings(names)
	for _, name := range names[:len(names)-HistoryRecords] {
		os.Remove(filepath.Join(dir, name))
	}
	_ = syncDir(dir)
}

func truncate(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > max {
		return s[:max]
	}
	return s
}

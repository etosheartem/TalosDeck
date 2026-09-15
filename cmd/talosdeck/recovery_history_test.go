package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talosdeck/internal/drtarget"
	"talosdeck/internal/recovery"
)

// A recovery command that cannot reach its target must still leave evidence that it
// ran and failed; silence is indistinguishable from a drill that was never scheduled.
func TestDrillRecordsFailedObservationWithTargetLocator(t *testing.T) {
	dir := t.TempDir()
	history := filepath.Join(dir, "history")
	target := filepath.Join(dir, "target.json")
	raw, _ := json.Marshal(drtarget.Config{Type: "ssh", Host: "unreachable-recovery-target.invalid", Directory: "/srv/dr"})
	if err := os.WriteFile(target, raw, 0600); err != nil {
		t.Fatal(err)
	}
	object := "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f.tdr"
	receipt := filepath.Join(dir, "receipt.json")
	raw, _ = json.Marshal(map[string]any{"receipt": drtarget.Receipt{Object: object, SHA256: strings.Repeat("ab", 32), Size: 4096}})
	if err := os.WriteFile(receipt, raw, 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "master.key")
	if err := os.WriteFile(key, []byte(`{"active":"k","keys":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recoveryCommand([]string{"drill", "--key", key, "--target", target, "--receipt", receipt, "--history", history, "--timeout", "20s"}); err == nil {
		t.Fatal("unreachable target reported success")
	}
	records, err := recovery.Load(history)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("drill left no observation: %d", len(records))
	}
	r := records[0]
	if r.Kind != "drill" || r.Outcome != "failed" {
		t.Fatal("wrong observation", r)
	}
	if r.Target != "ssh://unreachable-recovery-target.invalid/srv/dr/"+object {
		t.Fatal("target locator missing or wrong", r.Target)
	}
	// A failed download proves nothing about the copy.
	if r.RestoreTestedAt != nil || r.ChecksumVerifiedAt != nil || !r.BackupCreatedAt.IsZero() {
		t.Fatal("failed drill claimed verified properties", r)
	}
	if r.Error == "" {
		t.Fatal("failure reason not retained")
	}
}

// Credentials reach the CLI through the target file and must never be copied into
// the history a console reads.
func TestTargetLocatorOmitsCredentials(t *testing.T) {
	locator := targetLocator(drtarget.Config{Type: "s3", Endpoint: "https://s3.example", Bucket: "dr", AccessKey: "AKIA", SecretKey: "top-secret"}, "object.tdr")
	if locator != "s3://dr/object.tdr" {
		t.Fatal(locator)
	}
	if strings.Contains(locator, "top-secret") || strings.Contains(locator, "AKIA") {
		t.Fatal("credentials leaked into the locator")
	}
	if targetLocator(drtarget.Config{Type: "s3", Bucket: "dr"}, "") != "" {
		t.Fatal("locator invented for an object that was never named")
	}
}

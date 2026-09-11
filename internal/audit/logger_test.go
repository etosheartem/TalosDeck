package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditManager(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "audit-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logFile := filepath.Join(tempDir, "test-audit.log")
	am, err := NewAuditManager(logFile, 5)
	if err != nil {
		t.Fatalf("failed to create AuditManager: %v", err)
	}
	defer am.Close()

	// 1. Log events
	events := []AuditEvent{
		{Action: "auth.login", User: "admin", IP: "127.0.0.1", Status: "success"},
		{Action: "node.reboot", User: "admin", IP: "192.168.1.50", Status: "success", Details: map[string]any{"node": "10.42.0.110"}},
		{Action: "backup.create", User: "admin", IP: "127.0.0.1", Status: "failed", Details: map[string]any{"error": "disk full"}},
		{Action: "worker.create", User: "operator", IP: "10.0.0.5", Status: "success", Details: map[string]any{"vmid": 115}},
		{Action: "auth.login", User: "viewer", IP: "127.0.0.1", Status: "failed"},
		{Action: "worker.delete", User: "admin", IP: "10.0.0.5", Status: "success", Details: map[string]any{"vmid": 115}},
	}

	for _, e := range events {
		am.Log(e)
	}

	// Because maxEntries is 5 and we logged 6 events, total in memory should be 5
	if am.TotalCount() != 5 {
		t.Errorf("expected 5 events in memory, got %d", am.TotalCount())
	}

	// 2. Query all events (limit 10)
	all := am.GetEvents(10, "", "")
	if len(all) != 5 {
		t.Errorf("expected 5 events, got %d", len(all))
	}
	// Newest first -> worker.delete should be first
	if all[0].Action != "worker.delete" {
		t.Errorf("expected first event to be worker.delete, got %s", all[0].Action)
	}

	// 3. Query by action
	workerEvents := am.GetEvents(10, "worker", "")
	if len(workerEvents) != 2 {
		t.Errorf("expected 2 worker events, got %d", len(workerEvents))
	}

	// 4. Query by search (e.g. "failed")
	failedEvents := am.GetEvents(10, "", "failed")
	if len(failedEvents) != 2 {
		t.Errorf("expected 2 failed events, got %d", len(failedEvents))
	}

	// 5. Test persistence on reload
	_ = am.Close()
	reloaded, err := NewAuditManager(logFile, 100)
	if err != nil {
		t.Fatalf("failed to reload AuditManager: %v", err)
	}
	defer reloaded.Close()

	if reloaded.TotalCount() != 6 {
		t.Errorf("expected 6 events loaded from file, got %d", reloaded.TotalCount())
	}
}

func TestAuditSecurityAndRace(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "audit-sec-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logFile := filepath.Join(tempDir, "audit.log")
	am, err := NewAuditManager(logFile, 50)
	if err != nil {
		t.Fatalf("failed to create AuditManager: %v", err)
	}
	defer am.Close()

	// 1. Check file permissions (SEC-11)
	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("failed to stat log file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected file mode 0600, got %o", perm)
	}

	// 2. Sensitive data masking (SEC-12)
	am.Log(AuditEvent{
		Action: "auth.login",
		Details: map[string]any{
			"password": "supersecretpassword",
			"token":    "ey12345",
			"error":    "failed with bearer secret-token-xyz",
			"node":     "10.42.0.110",
		},
	})
	events := am.GetEvents(1, "auth.login", "")
	if len(events) != 1 {
		t.Fatalf("expected 1 event")
	}
	d := events[0].Details
	if d["password"] != "***MASKED***" {
		t.Errorf("expected password to be masked, got %v", d["password"])
	}
	if d["token"] != "***MASKED***" {
		t.Errorf("expected token to be masked, got %v", d["token"])
	}
	if strErr, ok := d["error"].(string); !ok || !strings.Contains(strErr, "***MASKED***") {
		t.Errorf("expected bearer token in error to be masked, got %v", d["error"])
	}
	if d["node"] != "10.42.0.110" {
		t.Errorf("expected non-sensitive node to be preserved, got %v", d["node"])
	}

	// Sensitive values and nested maps inside slices are sanitized and copied (SEC-16).
	nested := map[string]any{"token": "secret-token", "value": "safe"}
	detailSlice := []any{nested, "bearer nested-secret"}
	am.Log(AuditEvent{Action: "slice.details", Details: map[string]any{"items": detailSlice}})
	nested["value"] = "mutated"
	detailSlice[1] = "mutated"

	sliceEvents := am.GetEvents(1, "slice.details", "")
	if len(sliceEvents) != 1 {
		t.Fatalf("expected one slice.details event")
	}
	items, ok := sliceEvents[0].Details["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected copied []any details, got %#v", sliceEvents[0].Details["items"])
	}
	itemMap, ok := items[0].(map[string]any)
	if !ok || itemMap["token"] != "***MASKED***" || itemMap["value"] != "safe" {
		t.Errorf("nested slice map was not sanitized and copied: %#v", items[0])
	}
	if secret, ok := items[1].(string); !ok || !strings.Contains(secret, "***MASKED***") {
		t.Errorf("slice string was not sanitized: %#v", items[1])
	}

	items[0].(map[string]any)["value"] = "reader mutation"
	reloadedSlice := am.GetEvents(1, "slice.details", "")[0].Details["items"].([]any)
	if reloadedSlice[0].(map[string]any)["value"] != "safe" {
		t.Error("GetEvents returned a slice sharing internal nested map state")
	}

	// 3. Race condition & defensive copy test (SEC-04)
	detailsMap := map[string]any{"counter": 0}
	am.Log(AuditEvent{Action: "race.test", Details: detailsMap})

	// Mutate original map outside
	detailsMap["counter"] = 999
	evs := am.GetEvents(1, "race.test", "")
	if len(evs) > 0 && evs[0].Details["counter"] == 999 {
		t.Errorf("defensive copy failed: internal state was modified via caller map!")
	}

	// Concurrent read and write to verify race-free behavior
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			am.Log(AuditEvent{
				Action:  "concurrent.log",
				Details: map[string]any{"idx": i, "val": "something"},
			})
		}
		close(done)
	}()

	for {
		select {
		case <-done:
			return
		default:
			evList := am.GetEvents(10, "", "")
			for _, e := range evList {
				if e.Details != nil {
					e.Details["mutated_by_reader"] = true
				}
			}
		}
	}
}

func TestLargeAuditLine(t *testing.T) {
	// SEC-10: Large entry > 64KB
	tempDir, err := os.MkdirTemp("", "audit-large-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logFile := filepath.Join(tempDir, "audit.log")
	am, err := NewAuditManager(logFile, 10)
	if err != nil {
		t.Fatalf("failed to create AuditManager: %v", err)
	}

	largeString := strings.Repeat("A", 128*1024) // 128KB string (exceeds default 64KB bufio.Scanner)
	am.Log(AuditEvent{
		Action:  "large.payload",
		Details: map[string]any{"data": largeString},
	})
	_ = am.Close()

	// Reload from file to ensure it was parsed without scanner error
	reloaded, err := NewAuditManager(logFile, 10)
	if err != nil {
		t.Fatalf("failed to reload AuditManager: %v", err)
	}
	defer reloaded.Close()

	if reloaded.TotalCount() != 1 {
		t.Errorf("expected 1 large event loaded, got %d", reloaded.TotalCount())
	}
}

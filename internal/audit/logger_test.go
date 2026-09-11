package audit

import (
	"os"
	"path/filepath"
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

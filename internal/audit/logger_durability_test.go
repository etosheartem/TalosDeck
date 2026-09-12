package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordFailureIsStickyAndNeverPublished(t *testing.T) {
	for _, failure := range []string{"marshal", "cycle", "write", "sync"} {
		t.Run(failure, func(t *testing.T) {
			m, err := NewAuditManager(filepath.Join(t.TempDir(), "audit.log"), 4)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			event := AuditEvent{Action: "test.failure"}
			switch failure {
			case "marshal":
				event.Details = map[string]any{"unsupported": make(chan int)}
			case "cycle":
				event.Details = map[string]any{}
				event.Details["recursive"] = event.Details
			case "write":
				if err := m.logFile.Close(); err != nil {
					t.Fatal(err)
				}
			case "sync":
				if err := m.logFile.Close(); err != nil {
					t.Fatal(err)
				}
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				m.logFile = writer
			}
			if err := m.Record(event); err == nil {
				t.Fatal("failure was hidden")
			}
			if m.TotalCount() != 0 {
				t.Fatal("failed record was published in memory")
			}
			if m.Health() == nil {
				t.Fatal("journal failure missing from health")
			}
			if err := m.Record(AuditEvent{Action: "later.success"}); err == nil {
				t.Fatal("sticky failure was cleared by a subsequent record")
			}
		})
	}
}

func TestStartupRingIsBoundedAndRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10000; i++ {
		if _, err := fmt.Fprintf(f, "{\"id\":\"%d\",\"action\":\"history\"}\n", i); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := NewAuditManager(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.events) != 3 || cap(m.events) > 3 {
		t.Fatalf("startup ring is not bounded: len=%d cap=%d", len(m.events), cap(m.events))
	}
	events := m.GetEvents(3, "", "")
	if events[0].ID != "9999" || events[2].ID != "9997" {
		t.Fatalf("incorrect newest history: %+v", events)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"{\"action\":", strings.Repeat("x", maxAuditLineBytes+1)} {
		if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		if loaded, err := NewAuditManager(path, 3); err == nil {
			loaded.Close()
			t.Fatal("corrupt/oversized journal silently loaded")
		}
	}
}

func TestRecordNormalizesTypedDetailsBeforeDurableWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	m, err := NewAuditManager(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	typed := map[string]string{"password": "must-not-leak", "value": "original"}
	if err := m.Record(AuditEvent{Action: "typed.details", Details: map[string]any{"nested": typed}}); err != nil {
		t.Fatal(err)
	}
	typed["value"] = "caller changed"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-leak") {
		t.Fatal("typed map escaped sanitization")
	}
	var persisted AuditEvent
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.ID == "" || persisted.Timestamp.IsZero() {
		t.Fatal("durable metadata missing")
	}
	got := m.GetEvents(1, "", "")[0].Details["nested"].(map[string]any)
	if got["value"] != "original" || got["password"] != "***MASKED***" {
		t.Fatal("typed map was not deeply copied and sanitized")
	}
	if m.Health() != nil {
		t.Fatal("healthy journal reported failure")
	}
}

func TestRepeatedStringSecretsAreRedactedDurably(t *testing.T) {
	for _, input := range []string{
		"Bearer alpha Bearer beta; bearer gamma",
		"İ журнал TOKEN=alpha TOKEN=beta",
		"token=alpha&token=beta;TOKEN=gamma",
		"password=alpha, password=beta secret=gamma secret=delta",
		"token= token=alpha token=",
		"Bearer ***MASKED*** Bearer alpha",
	} {
		t.Run(input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.log")
			m, err := NewAuditManager(path, 4)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.Record(AuditEvent{Action: "test", Details: map[string]any{"message": input, "nested": []string{input}}}); err != nil {
				t.Fatal(err)
			}
			if err = m.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"alpha", "beta", "gamma", "delta"} {
				if strings.Contains(string(data), secret) {
					t.Fatalf("secret %s persisted in audit", secret)
				}
			}
			if got := sanitizeStringValue(sanitizeStringValue(input)); got != sanitizeStringValue(input) {
				t.Fatal("redaction is not idempotent")
			}
		})
	}
}

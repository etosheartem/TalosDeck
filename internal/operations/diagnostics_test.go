package operations

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestSupportBundleUsesOnlyDiagnosticFindings(t *testing.T) {
	b := backupFixture(t)
	ctx := context.Background()
	s := &DiagnosticsService{ClusterID: b.ClusterID, Store: b.Store}
	if _, err := s.Bundle(ctx); err == nil {
		t.Fatal("empty report exported as diagnostics")
	}
	// Credentials and raw log data elsewhere in the encrypted store must not be
	// swept into an archive by a broad directory or database export.
	b.put(ctx, "credentials", "sample", map[string]string{"privateKey": "PRIVATE-KEY-CANARY", "token": "TOKEN-CANARY", "secret": "SECRET-CANARY"})
	report := DiagnosticReport{Status: "degraded", CheckedAt: time.Now(), Checks: []DiagnosticCheck{{ID: "disk", Severity: "warning", Component: "storage", Node: "10.0.0.1", Title: "Filesystem nearly full", Details: "/var: 91% used"}}, Summary: DiagnosticSummary{Warning: 1}}
	data, _ := json.Marshal(report)
	s.Store.PutSecret(ctx, s.ClusterID, "diagnostics", "latest", data)
	archive, err := s.Bundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name != "diagnostics.json" && hdr.Name != "README.txt" {
			t.Fatal("unexpected archive entry")
		}
		body, _ := io.ReadAll(tr)
		for _, secret := range []string{"PRIVATE-KEY-CANARY", "TOKEN-CANARY", "SECRET-CANARY"} {
			if strings.Contains(string(body), secret) {
				t.Fatal("credential leaked")
			}
		}
		count++
	}
	if count != 2 {
		t.Fatal("missing support entries")
	}
}

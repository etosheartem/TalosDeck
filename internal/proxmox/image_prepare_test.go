package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFactoryDownloadPinsChecksumAndNeverOverwrites(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[exists], func(t *testing.T) {
			filename := "talosdeck-12345678-1234-1234-1234-123456789abc.iso"
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					data := []any{}
					if exists {
						data = append(data, map[string]any{"volid": "data:iso/" + filename})
					}
					json.NewEncoder(w).Encode(map[string]any{"data": data})
					return
				}
				posts++
				r.ParseForm()
				for key, want := range map[string]string{"content": "iso", "filename": filename, "checksum": strings.Repeat("b", 64), "checksum-algorithm": "sha256", "verify-certificates": "1"} {
					if r.Form.Get(key) != want {
						t.Errorf("%s=%q", key, r.Form.Get(key))
					}
				}
				if r.URL.Path != "/api2/json/nodes/pve/storage/data/download-url" {
					t.Errorf("path %s", r.URL.Path)
				}
				json.NewEncoder(w).Encode(map[string]any{"data": "UPID:test"})
			}))
			defer server.Close()
			c, err := NewClient(Config{BaseURL: server.URL, Node: "pve", APIToken: "user@pam!test=value"})
			if err != nil {
				t.Fatal(err)
			}
			task, err := c.DownloadISO(context.Background(), "data", filename, "https://factory.talos.dev/image/"+strings.Repeat("a", 64)+"/v1.14.0/metal-amd64.iso", strings.Repeat("b", 64))
			if exists {
				if err == nil || posts != 0 {
					t.Fatal("existing asset overwritten")
				}
			} else if err != nil || posts != 1 || task != "UPID:test" {
				t.Fatalf("download %s %v", task, err)
			}
		})
	}
}
func TestFactoryPreflightIgnoresLegacyISOButRequiresISOStorage(t *testing.T) {
	for _, content := range []string{"iso,vztmpl", "images"} {
		t.Run(content, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/content") {
					t.Error("looked up legacy ISO")
				}
				var data any = map[string]any{"memory": map[string]any{"available": uint64(8) << 30}}
				if strings.Contains(r.URL.Path, "/storage/") {
					data = map[string]any{"active": 1, "enabled": 1, "avail": uint64(100) << 30, "content": content}
				}
				json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer server.Close()
			c, err := NewClient(Config{BaseURL: server.URL, Node: "pve", APIToken: "user@pam!test=value", DefaultISO: "local:iso/nonexistent.iso"})
			if err != nil {
				t.Fatal(err)
			}
			err = c.PreflightFactoryMachines(context.Background(), []MachineSpec{testMachine()}, "data")
			if (err == nil) != (content == "iso,vztmpl") {
				t.Fatalf("preflight %v", err)
			}
		})
	}
}

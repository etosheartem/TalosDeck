package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreflightRejectsCapacityAndMissingISOWithoutMutation(t *testing.T) {
	for _, mode := range []string{"ready", "ram", "disk", "iso"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("preflight mutated provider")
				}
				var data any
				switch {
				case strings.Contains(r.URL.Path, "/content"):
					vol := "local:iso/talos.iso"
					if mode == "iso" {
						vol = "local:iso/other.iso"
					}
					data = []any{map[string]any{"volid": vol}}
				case strings.Contains(r.URL.Path, "/storage/"):
					avail := uint64(100) << 30
					if mode == "disk" {
						avail = 1 << 30
					}
					data = map[string]any{"active": 1, "enabled": 1, "avail": avail}
				default:
					available := uint64(8) << 30
					if mode == "ram" {
						available = 1 << 30
					}
					data = map[string]any{"memory": map[string]any{"available": available}}
				}
				json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer s.Close()
			c, err := NewClient(Config{BaseURL: s.URL, Node: "pve", APIToken: "user@pam!test=value"})
			if err != nil {
				t.Fatal(err)
			}
			err = c.PreflightMachines(context.Background(), []MachineSpec{testMachine(), testMachine()})
			if (err == nil) != (mode == "ready") {
				t.Fatalf("unexpected preflight: %v", err)
			}
		})
	}
}

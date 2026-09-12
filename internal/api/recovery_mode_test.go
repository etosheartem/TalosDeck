package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestRecoveryMarkerFailClosedAndLatched(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "recovery-required.json")
	safe, err := RecoveryRequired(dir)
	if err != nil || safe {
		t.Fatal(safe, err)
	}
	if err := os.WriteFile(marker, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	safe, err = RecoveryRequired(dir)
	if err != nil || !safe {
		t.Fatal(safe, err)
	}
	app := fiber.New()
	app.Use(recoveryGuard(safe))
	app.Post("/api/jobs", func(c *fiber.Ctx) error { t.Fatal("mutation reached handler"); return nil })
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(httptest.NewRequest("POST", "/api/jobs", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 423 {
		t.Fatal(resp.StatusCode)
	}
	if err := os.Symlink(filepath.Join(dir, "absent"), marker); err != nil {
		t.Fatal(err)
	}
	safe, err = RecoveryRequired(dir)
	if err != nil || !safe {
		t.Fatal("dangling sentinel must fail closed", safe, err)
	}
}

func TestRecoveryGuardProtectsFleetLegacyAndTransport(t *testing.T) {
	cases := []struct {
		method, path string
		blocked      bool
	}{
		{"POST", "/api/clusters/abc/nodes/10.0.0.1/reboot", true},
		{"POST", "/api/provision/jobs", true}, {"DELETE", "/api/providers/id", true},
		{"PATCH", "/api/auth/users/id", true}, {"GET", "/ws/nodes/ip/logs/kubelet", true},
		{"GET", "/api/clusters/id/ws/exec", true},
		{"POST", "/api/auth/login", false}, {"POST", "/api/auth/logout", false},
		{"GET", "/api/clusters", false}, {"GET", "/api/jobs", false},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			app := fiber.New()
			app.Use(recoveryGuard(true))
			reached := false
			app.Use(func(c *fiber.Ctx) error { reached = true; return c.SendStatus(200) })
			resp, err := app.Test(httptest.NewRequest(tc.method, tc.path, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if tc.blocked && (reached || resp.StatusCode != 423) {
				t.Fatal("unsafe request allowed", resp.StatusCode)
			}
			if !tc.blocked && (!reached || resp.StatusCode != 200) {
				t.Fatal("read/login blocked", resp.StatusCode)
			}
		})
	}
}

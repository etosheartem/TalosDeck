package api

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/audit"
)

func TestAuditFailurePreventsMutationButPreservesReadAccess(t *testing.T) {
	m, err := audit.NewAuditManager(filepath.Join(t.TempDir(), "audit.log"), 20)
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	app.Use(auditMutationGuard(m, nil, false))
	mutations := 0
	app.Post("/operation", func(c *fiber.Ctx) error { mutations++; return c.SendStatus(200) })
	app.Get("/read", func(c *fiber.Ctx) error { return c.SendStatus(200) })
	app.Get("/readyz", func(c *fiber.Ctx) error { return c.SendStatus(200) })
	request := func(method, path string, want int) {
		t.Helper()
		r, err := app.Test(httptest.NewRequest(method, path, nil))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d", method, path, r.StatusCode, want)
		}
	}
	request("POST", "/operation", 200)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	request("POST", "/operation", 503)
	request("GET", "/read", 200)
	request("GET", "/readyz", 503)
	if mutations != 1 {
		t.Fatalf("unaudited mutation accepted: %d", mutations)
	}
}

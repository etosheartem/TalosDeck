package api

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v2"
)

type unavailableAdmissionAuthority struct{ calls atomic.Int32 }

func (a *unavailableAdmissionAuthority) Validate(context.Context, string, uint64) error {
	a.calls.Add(1)
	return errors.New("authority unavailable")
}

func TestFleetRequiredAuthorityBlocksBeforeHandlerAndPreservesRecoveryReads(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "missing"
		if configured {
			name = "unavailable"
		}
		t.Run(name, func(t *testing.T) {
			f := newFleetFixture(t)
			f.fleet.options.RequireExecutionAuthority = true
			authority := &unavailableAdmissionAuthority{}
			if configured {
				f.fleet.options.ExecutionAuthority = authority
				f.fleet.options.ManagementInstanceID = "test"
				f.fleet.options.ExecutionEpoch = 42
			}
			var writes atomic.Int32
			f.app.Post("/authority-mutation-probe", func(c *fiber.Ctx) error { writes.Add(1); return c.SendStatus(204) })
			if code, _ := fleetRequest(t, f.app, "POST", "/authority-mutation-probe", f.token, nil); code != 423 {
				t.Fatalf("mutation admitted: %d", code)
			}
			if writes.Load() != 0 {
				t.Fatal("handler ran without authority")
			}
			if code, _ := fleetRequest(t, f.app, "POST", "/api/clusters", f.token, map[string]string{"name": "should-not-import"}); code != 423 {
				t.Fatalf("infrastructure handler admitted: %d", code)
			}
			before := authority.calls.Load()
			for _, path := range []string{"/api/health", "/api/clusters", "/api/auth/me"} {
				if code, _ := fleetRequest(t, f.app, "GET", path, f.token, nil); code != 200 {
					t.Fatalf("read %s blocked: %d", path, code)
				}
			}
			if code, _ := fleetRequest(t, f.app, "POST", "/api/auth/login", "", map[string]string{"password": "fleet-test-password"}); code != 200 {
				t.Fatalf("login blocked: %d", code)
			}
			// An absent job should report its normal not-found error, not lease denial.
			if code, _ := fleetRequest(t, f.app, "POST", "/api/provision/jobs/11111111-1111-4111-8111-111111111111/reconcile", f.token, nil); code != 404 {
				t.Fatalf("read-only reconcile blocked: %d", code)
			}
			if authority.calls.Load() != before {
				t.Fatal("read/auth/reconcile contacted unavailable authority")
			}
		})
	}
}

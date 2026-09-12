package api

import (
	"context"
	"github.com/gofiber/fiber/v2"
	"net/http/httptest"
	"talosdeck/internal/auth"
	"talosdeck/internal/jobs"
	"talosdeck/internal/operations"
	"testing"
	"time"
)

func TestReconcileEndpointSafeModeStillAuthenticatesAndScopesJobs(t *testing.T) {
	m, err := jobs.OpenCluster(t.TempDir(), "scope", func(context.Context, *jobs.Execution, jobs.Request) error { return jobs.ErrUncertain })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, err := m.Submit(jobs.Request{Kind: "cluster-create"}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 100; n++ {
		j, _ = m.Get(j.ID)
		if j.Status == "interrupted" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	am := auth.NewAuthManager("password-password", "testsecretjwtkey1234567890123456")
	for _, prefix := range []string{"/api", "/api/provision", "/api/clusters/11111111-1111-4111-8111-111111111111"} {
		app := fiber.New()
		app.Use(recoveryGuard(true))
		RegisterJobRoutes(app.Group(prefix), m, &operations.Service{}, am)
		for _, tc := range []struct {
			role string
			want int
		}{{"", 401}, {"viewer", 403}, {"operator", 403}, {"admin", 409}} {
			req := httptest.NewRequest("POST", prefix+"/jobs/"+j.ID+"/reconcile", nil)
			if tc.role != "" {
				token, _ := am.GenerateToken("fixture", tc.role)
				req.Header.Set("Authorization", "Bearer "+token)
			}
			res, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("%s %s: %d want %d", prefix, tc.role, res.StatusCode, tc.want)
			}
		}
		req := httptest.NewRequest("POST", prefix+"/jobs/22222222-2222-4222-8222-222222222222/reconcile", nil)
		token, _ := am.GenerateToken("fixture", "admin")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatal("foreign job accessible", res.StatusCode)
		}
	}
}

type rejectedAuthority struct{}

func (rejectedAuthority) Validate(context.Context, string, uint64) error { return jobs.ErrUncertain }
func TestFleetLostAuthorityPermitsOnlyReadOnlyReconcile(t *testing.T) {
	f := newFleetFixture(t)
	f.fleet.options.ExecutionAuthority = rejectedAuthority{}
	path := "/api/provision/jobs/22222222-2222-4222-8222-222222222222/reconcile"
	status, _ := fleetRequest(t, f.app, "POST", path, f.token, nil)
	if status != 404 {
		t.Fatal("read-only reconcile blocked by lost execution lease", status)
	}
	status, _ = fleetRequest(t, f.app, "POST", "/api/clusters", f.token, nil)
	if status != 423 {
		t.Fatal("mutation bypassed lost lease", status)
	}
}

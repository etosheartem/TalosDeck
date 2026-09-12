package api

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/jobs"
	"talosdeck/internal/operations"
)

type replacementAPIStub struct {
	calls     int
	lastActor string
	approved  bool
	fail      bool
	kind      string
}

func (s *replacementAPIStub) PlanReplacement(_ context.Context, _ operations.ReplacementSpec, user string) (*operations.WorkerReplacementPlan, error) {
	s.calls++
	s.lastActor = user
	if s.fail {
		return nil, errors.New("replacement clients unavailable")
	}
	return &operations.WorkerReplacementPlan{}, nil
}
func (s *replacementAPIStub) ListReplacements(context.Context) ([]operations.WorkerReplacementPlan, error) {
	s.calls++
	return []operations.WorkerReplacementPlan{}, nil
}
func (s *replacementAPIStub) ApproveReplacement(_ context.Context, id, name, hash, user string, ack bool) (jobs.Request, error) {
	s.calls++
	s.lastActor = user
	if id != "plan" || name != "worker" || hash != "reviewed-hash" || !ack {
		return jobs.Request{}, errors.New("confirmation or impact hash does not match reviewed plan")
	}
	s.approved = true
	return jobs.Request{Kind: s.kind}, nil
}
func (s *replacementAPIStub) PlanReplacementResume(_ context.Context, _ string, user string) (*operations.WorkerReplacementPlan, error) {
	s.calls++
	s.lastActor = user
	return &operations.WorkerReplacementPlan{}, nil
}
func TestReplacementRoutesAuthorizationScopeAndStrictConfirmation(t *testing.T) {
	manager, e := jobs.OpenCluster(t.TempDir(), "cluster-a", func(context.Context, *jobs.Execution, jobs.Request) error { return jobs.ErrUncertain })
	if e != nil {
		t.Fatal(e)
	}
	defer manager.Close()
	if e = manager.SetExecutionAuthority(permittedRecoveryAuthority{}, "replacement-test", 1); e != nil {
		t.Fatal(e)
	}
	am := auth.NewAuthManager("test-password", "test-jwt-secret")
	stub := &replacementAPIStub{kind: "worker-replace"}
	app := fiber.New()
	registerReplacementRoutes(app.Group("/api/clusters/cluster-a"), manager, stub, "cluster-a", am)
	request := func(role, method, path, body string, want int) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/clusters/cluster-a/replacements"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if role != "" {
			token, _ := am.GenerateToken("fixture", role)
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, e := app.Test(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s %s => %d want%d: %s", role, method, path, res.StatusCode, want, raw)
		}
	}
	for _, role := range []string{"", "viewer", "operator"} {
		want := 403
		if role == "" {
			want = 401
		}
		request(role, "GET", "", "", want)
		request(role, "POST", "/plan", `{}`, want)
		request(role, "POST", "", `{}`, want)
		request(role, "POST", "/plan/resume-plan", `{}`, want)
	}
	if stub.calls != 0 {
		t.Fatal("unauthorized request reached planner")
	}
	request("admin", "GET", "", "", 200)
	request("admin", "POST", "/plan", `{"machineID":"x","kubeconfig":"/etc/private"}`, 400)
	request("admin", "POST", "/plan", `{"machineId":"x","replacement":{"masterKeyPath":"/etc/private"}}`, 400)
	request("admin", "POST", "/plan", `{} {}`, 400)
	request("admin", "POST", "/plan", `null`, 400)
	request("admin", "POST", "/plan/resume-plan", `{"executorEpoch":99}`, 400)
	request("admin", "POST", "", `{"planId":"plan","confirmedName":"worker"}`, 400)
	request("admin", "POST", "", `{"planId":"plan","confirmedName":"worker","impactHash":"stale","acknowledgeStorageImpact":true}`, 409)
	if stub.approved {
		t.Fatal("stale storage review approved")
	}
	request("admin", "POST", "/plan/resume-plan", `{}`, 200)
	if stub.lastActor != "fixture" {
		t.Fatal("actor not derived from authentication")
	}
	stub.fail = true
	request("admin", "POST", "/plan", `{}`, 422)
	// Reject accidental dispatch of any other workflow even if the planner errs.
	stub.kind = "cluster-create"
	request("admin", "POST", "", `{"planId":"plan","confirmedName":"worker","impactHash":"reviewed-hash","acknowledgeStorageImpact":true}`, 503)
	stub.kind = "worker-replace"
	request("admin", "POST", "", `{"planId":"plan","confirmedName":"worker","impactHash":"reviewed-hash","acknowledgeStorageImpact":true}`, 202)
	queued := manager.List()
	if len(queued) != 1 || queued[0].Request.Kind != "worker-replace" || queued[0].ClusterID != "cluster-a" || queued[0].User != "fixture" {
		t.Fatalf("replacement job lost scope or actor: %+v", queued)
	}
}
func TestReplacementRoutesUnavailableAndFleetScopeFailClosed(t *testing.T) {
	am := auth.NewAuthManager("test-password", "test-jwt-secret")
	token, _ := am.GenerateToken("fixture", "admin")
	for _, tc := range []struct {
		scope   string
		service replacementPlanner
		want    int
	}{{"", &replacementAPIStub{}, 409}, {operations.FleetScope, &replacementAPIStub{}, 409}, {"cluster-a", nil, 503}} {
		app := fiber.New()
		registerReplacementRoutes(app.Group("/api"), nil, tc.service, tc.scope, am)
		req := httptest.NewRequest("POST", "/api/replacements/plan", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, e := app.Test(req)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatalf("scope %q: got%d want%d", tc.scope, res.StatusCode, tc.want)
		}
	}
}

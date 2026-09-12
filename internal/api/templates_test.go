package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"talosdeck/internal/auth"
	"talosdeck/internal/clusters"
	"talosdeck/internal/operations"
	"talosdeck/internal/templates"
)

type templatePlanFixture struct{ plans []operations.ProvisionPlan }

func (f *templatePlanFixture) PlanFromTemplate(_ context.Context, spec operations.ProvisionSpec, _ string, ref operations.TemplateReference) (*operations.ProvisionPlan, error) {
	p := operations.ProvisionPlan{ID: uuid.NewString(), Spec: spec, Template: &ref}
	f.plans = append(f.plans, p)
	return &p, nil
}
func TestTemplateRoutesPinRevisionAndRejectOverrides(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := clusters.Open(filepath.Join(dir, "db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := templates.Open(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	defaults := templates.MachineDefaults{Cores: 2, MemoryMB: 2048, DiskGB: 20, NetworkMode: "dhcp"}
	spec := templates.Spec{TalosVersion: "v1.14.0", KubernetesVersion: "v1.37.0", SchematicID: strings.Repeat("a", 64), Architecture: "amd64", Platform: "metal", CNI: "flannel", Storage: "none", ControlPlanes: 1, ControlPlane: defaults, Worker: defaults}
	first, err := service.Create(ctx, "fixture", spec, "admin")
	if err != nil {
		t.Fatal(err)
	}
	spec.ControlPlane.Cores = 4
	second, err := service.Revise(ctx, first.TemplateID, 1, "edited", spec, "admin")
	if err != nil {
		t.Fatal(err)
	}
	am := auth.NewAuthManager("test-password", "test-jwt-key")
	app := fiber.New()
	planner := &templatePlanFixture{}
	RegisterTemplateRoutes(app.Group("/api"), service, planner, am)
	perform := func(role, method, path, body string, want int) []byte {
		t.Helper()
		token, _ := am.GenerateToken("fixture", role)
		req := httptest.NewRequest(method, "/api/templates"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res, e := app.Test(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s got%d want%d: %s", role, path, res.StatusCode, want, b)
		}
		return b
	}
	perform("viewer", "GET", "", "", 200)
	perform("operator", "POST", "", `{}`, 403)
	perform("admin", "POST", "", `{"name":"bad","spec":{"kubeconfig":"secret"}}`, 400)
	path := "/" + first.TemplateID + "/revisions/1/plans"
	perform("viewer", "POST", path, `{}`, 403)
	perform("admin", "POST", path, `{"name":"created","talosVersion":"v9.0.0"}`, 400)
	input := templates.Input{Name: "created", ProviderID: uuid.NewString(), ISOStorage: "local", Machines: []templates.InstanceMachine{{Name: "created-cp", Role: "controlplane"}}}
	raw, _ := json.Marshal(input)
	perform("admin", "POST", path, string(raw), 200)
	if len(planner.plans) != 1 || planner.plans[0].Spec.Machines[0].Cores != 2 || planner.plans[0].Template.SpecHash != first.SpecHash || planner.plans[0].Template.Revision != 1 {
		t.Fatal("latest revision replaced pinned snapshot")
	}
	if err := service.Archive(ctx, first.TemplateID, second.Revision, true); err != nil {
		t.Fatal(err)
	}
	perform("admin", "POST", path, string(raw), 409)
	perform("viewer", "GET", "/"+first.TemplateID+"/revisions", "", 200)
	if len(planner.plans) != 1 {
		t.Fatal("archived template created another plan")
	}
	if planner.plans[0].Template.Name != "fixture" {
		t.Fatal("template edit changed reviewed plan")
	}
}

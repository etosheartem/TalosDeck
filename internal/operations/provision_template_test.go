package operations

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"path/filepath"
	"strings"
	"talosdeck/internal/clusters"
	"talosdeck/internal/proxmox"
	"testing"
)

func TestTemplatePlanPinsProvenanceAndUsesExistingRunnerAfterRestart(t *testing.T) {
	s, spec, dir := provisionFixture(t)
	fake := &failCreateProvider{}
	s.ProviderFactory = func(ProviderRecord) (proxmox.MachineProvider, error) { return fake, nil }
	ref := TemplateReference{ID: uuid.NewString(), Revision: 3, SpecHash: strings.Repeat("a", 64), Name: "Production"}
	plan, err := s.PlanFromTemplate(context.Background(), spec, "admin", ref)
	if err != nil {
		t.Fatal(err)
	}
	originalName := spec.Machines[0].Name
	originalNS := spec.Machines[1].Nameservers[0]
	spec.Machines[0].Name = "changed"
	spec.Machines[1].Nameservers[0] = "192.0.2.1"
	ref.Name = "changed"
	ref.Revision = 4
	if plan.Spec.Machines[0].Name != originalName || plan.Spec.Machines[1].Nameservers[0] != originalNS || plan.Template.Revision != 3 || plan.Template.Name != "Production" {
		t.Fatal("plan aliases mutable caller data")
	}
	plan.Template.Name = "mutated response"
	plan.Spec.Machines[0].Name = "mutated-response"
	if err := s.Store.(*clusters.Store).Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := clusters.Open(filepath.Join(dir, "clusters.db"), filepath.Join(dir, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s.Store = reopened
	persisted, err := s.load(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Template == nil || persisted.Template.Revision != 3 || persisted.Template.Name != "Production" || persisted.Spec.Machines[0].Name != originalName {
		t.Fatal("restart lost immutable template provenance")
	}
	request, err := s.Request(context.Background(), plan.ID, persisted.Spec.Name, "admin")
	if err != nil {
		t.Fatal(err)
	}
	job := runJob(t, &Service{Provision: s}, request)
	if job.Status != "interrupted" || fake.created != 1 || job.Request.ProvisionID != plan.ID {
		t.Fatal("template plan did not use existing provisioning runner")
	}
	found := false
	for _, event := range job.Events {
		if event.Step == "template" && strings.Contains(event.Message, "revision 3") && strings.Contains(event.Message, strings.Repeat("a", 64)) {
			found = true
		}
	}
	if !found {
		t.Fatal("template provenance absent from job journal")
	}
	final, err := s.load(context.Background(), plan.ID)
	if err != nil || final.Template.Revision != 3 {
		t.Fatal("job changed template reference")
	}
}
func TestOrdinaryProvisionSpecCannotClaimTemplateProvenance(t *testing.T) {
	s, spec, _ := provisionFixture(t)
	fake := &failCreateProvider{}
	s.ProviderFactory = func(ProviderRecord) (proxmox.MachineProvider, error) { return fake, nil }
	encoded, _ := json.Marshal(spec)
	var raw map[string]any
	json.Unmarshal(encoded, &raw)
	raw["template"] = map[string]any{"id": uuid.NewString(), "revision": 3, "specHash": strings.Repeat("a", 64), "name": "forged"}
	encoded, _ = json.Marshal(raw)
	json.Unmarshal(encoded, &spec)
	plan, err := s.Plan(context.Background(), spec, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Template != nil {
		t.Fatal("client forged provenance")
	}
	for _, ref := range []TemplateReference{{}, {ID: uuid.NewString(), Revision: 0, SpecHash: strings.Repeat("a", 64), Name: "test"}, {ID: uuid.NewString(), Revision: 1, SpecHash: "invalid", Name: "test"}} {
		if _, err := s.PlanFromTemplate(context.Background(), spec, "admin", ref); err == nil {
			t.Fatal("invalid reference accepted")
		}
	}
}

type assertTemplateCreateStore struct {
	ProvisionStore
	t             *testing.T
	creates, puts int
}

func (s *assertTemplateCreateStore) CreateSecret(ctx context.Context, scope, kind, key string, data []byte) error {
	if kind == "provision-plan" {
		s.creates++
		var plan provisionState
		if err := json.Unmarshal(data, &plan); err != nil || plan.Template == nil || plan.Template.Revision != 3 {
			s.t.Fatal("initial durable create missing provenance")
		}
	}
	return s.ProvisionStore.CreateSecret(ctx, scope, kind, key, data)
}
func (s *assertTemplateCreateStore) PutSecret(ctx context.Context, scope, kind, key string, data []byte) error {
	if kind == "provision-plan" {
		s.puts++
	}
	return s.ProvisionStore.PutSecret(ctx, scope, kind, key, data)
}
func TestTemplateReferenceWrittenAtomicallyWithPlan(t *testing.T) {
	service, spec, _ := provisionFixture(t)
	store := &assertTemplateCreateStore{ProvisionStore: service.Store, t: t}
	service.Store = store
	service.ProviderFactory = func(ProviderRecord) (proxmox.MachineProvider, error) { return &failCreateProvider{}, nil }
	if _, err := service.PlanFromTemplate(context.Background(), spec, "admin", TemplateReference{ID: uuid.NewString(), Revision: 3, SpecHash: strings.Repeat("a", 64), Name: "Production"}); err != nil {
		t.Fatal(err)
	}
	if store.creates != 1 || store.puts != 0 {
		t.Fatalf("expected one atomic create, got %d creates/%d updates", store.creates, store.puts)
	}
}

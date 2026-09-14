package operations

import (
	"context"
	"errors"
	"talosdeck/internal/jobs"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/reconcile"
	"testing"
	"time"
)

type observeOnlyProvider struct {
	failCreateProvider
	unavailable bool
	reads       int
}

func (p *observeOnlyProvider) VerifyOwned(context.Context, proxmox.OwnedMachine) error {
	p.reads++
	if p.unavailable {
		return errors.New("provider credentials secret raw error")
	}
	return nil
}
func TestProviderReconcileObservesWithoutRetryAndRejectsForeignScope(t *testing.T) {
	s, spec, _ := provisionFixture(t)
	p := &observeOnlyProvider{}
	s.ProviderFactory = func(ProviderRecord) (proxmox.MachineProvider, error) { return p, nil }
	plan, err := s.Plan(context.Background(), spec, "admin")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.Request(context.Background(), plan.ID, spec.Name, "admin")
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Provision: s}
	m, err := jobs.OpenCluster(t.TempDir(), s.ClusterID, svc.Run)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, err := m.Submit(req, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3000; n++ {
		j, _ = m.Get(j.ID)
		if j.Status == "interrupted" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if j.Status != "interrupted" {
		t.Fatal(j)
	}
	observed, err := s.ReconcileJob(context.Background(), m, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Status != "interrupted" || observed.Reviewed || observed.Intents[0].Outcome != "succeeded" || p.created != 1 {
		t.Fatal("observation mutated infrastructure or review", observed)
	}
	p.unavailable = true
	observed, err = s.ReconcileJob(context.Background(), m, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Intents[0].Outcome != "UNKNOWN" || observed.Intents[0].Evidence.State != "unknown" {
		t.Fatal("outage mistaken for proof", observed)
	}
	foreign := ProvisionService{Store: s.Store, ClusterID: "other-cluster", ProviderFactory: s.ProviderFactory}
	reads := p.reads
	if _, err = foreign.ReconcileJob(context.Background(), m, j.ID); err == nil || p.reads != reads {
		t.Fatal("foreign scope queried provider")
	}
	if p.created != 1 || p.deleted != 0 {
		t.Fatal("reconciliation mutated provider")
	}
}

func TestProviderReconcileDoesNotInferOpaqueCommandSuccessFromVM(t *testing.T) {
	s, spec, _ := provisionFixture(t)
	p := &observeOnlyProvider{}
	s.ProviderFactory = func(ProviderRecord) (proxmox.MachineProvider, error) { return p, nil }
	plan, err := s.Plan(context.Background(), spec, "admin")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.Request(context.Background(), plan.ID, spec.Name, "admin")
	if err != nil {
		t.Fatal(err)
	}
	m, err := jobs.OpenCluster(t.TempDir(), s.ClusterID, func(ctx context.Context, e *jobs.Execution, _ jobs.Request) error {
		return reconcile.Mutate(ctx, "talos.reboot", "node", func() error { return errors.New("response lost") })
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, err := m.Submit(req, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3000; n++ {
		j, _ = m.Get(j.ID)
		if j.Status == "interrupted" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if j.Status != "interrupted" || len(j.Intents) != 1 {
		t.Fatal(j)
	}
	observed, err := s.ReconcileJob(context.Background(), m, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.reads != 0 || p.created != 0 || p.deleted != 0 || observed.Intents[0].Outcome != "UNKNOWN" || observed.ReconciliationOutcome != "requires_review" || observed.Reviewed {
		t.Fatalf("opaque command treated as provider state: %+v reads=%d", observed, p.reads)
	}
}

package templates

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"talosdeck/internal/clusters"
	"testing"
)

func fixtureSpec() Spec {
	d := MachineDefaults{Cores: 2, MemoryMB: 4096, DiskGB: 25, Storage: "local-lvm", Bridge: "vmbr0", NetworkMode: "dhcp"}
	return Spec{TalosVersion: "1.14.0", KubernetesVersion: "1.37.0", SchematicID: strings.Repeat("a", 64), Architecture: "amd64", Platform: "metal", CNI: "flannel", Storage: "none", ControlPlanes: 1, Workers: 1, ControlPlane: d, Worker: d}
}
func fixtureService(t *testing.T) (*Service, *clusters.Store) {
	t.Helper()
	d := t.TempDir()
	store, e := clusters.Open(filepath.Join(d, "db"), filepath.Join(d, "key"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	s, e := Open(context.Background(), store)
	if e != nil {
		t.Fatal(e)
	}
	return s, store
}
func TestImmutableRevisionConcurrentAndRestart(t *testing.T) {
	s, store := fixtureService(t)
	ctx := context.Background()
	r, e := s.Create(ctx, "production", fixtureSpec(), "admin")
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.Revise(ctx, r.TemplateID, 1, "new", fixtureSpec(), "admin")
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("lost update")
	}
	reopened, e := Open(ctx, store)
	if e != nil {
		t.Fatal(e)
	}
	old, e := reopened.GetRevision(r.TemplateID, 1)
	if e != nil || old.Name != "production" || old.SpecHash != r.SpecHash {
		t.Fatal("immutable revision changed")
	}
	old.Spec.Worker.Cores = 99
	again, _ := reopened.GetRevision(r.TemplateID, 1)
	if again.Spec.Worker.Cores == 99 {
		t.Fatal("alias")
	}
	if e = reopened.Archive(ctx, r.TemplateID, 1, true); !errors.Is(e, ErrConflict) {
		t.Fatal("stale archive accepted")
	}
	if e = reopened.Archive(ctx, r.TemplateID, 2, true); e != nil {
		t.Fatal(e)
	}
	if len(reopened.List(false)) != 0 {
		t.Fatal("archived visible")
	}
	if _, e = reopened.GetRevision(r.TemplateID, 1); e != nil {
		t.Fatal("archive destroyed history")
	}
}
func TestInstantiateUsesPinnedDefaults(t *testing.T) {
	s, _ := fixtureService(t)
	r, e := s.Create(context.Background(), "test", fixtureSpec(), "admin")
	if e != nil {
		t.Fatal(e)
	}
	in := Input{Name: "new-cluster", ProviderID: "provider", ISOStorage: "data", Machines: []InstanceMachine{{Name: "cp", Role: "controlplane"}, {Name: "worker", Role: "worker"}}}
	p, e := InstantiateSpec(r, in)
	if e != nil {
		t.Fatal(e)
	}
	if p.Machines[1].Cores != r.Spec.Worker.Cores || p.SchematicID != r.Spec.SchematicID {
		t.Fatal("defaults changed")
	}
	in.Machines[0].Name = "changed"
	if p.Machines[0].Name != "cp" {
		t.Fatal("aliased input")
	}
	r.Spec.Worker.Cores++
	if _, e = InstantiateSpec(r, in); !errors.Is(e, ErrInvalid) {
		t.Fatal("tampered revision accepted")
	}
}
func TestSpecValidation(t *testing.T) {
	for _, mutate := range []func(*Spec){func(s *Spec) { s.ControlPlanes = 2 }, func(s *Spec) { s.Workers = 100 }, func(s *Spec) { s.Worker.Cores = 0 }, func(s *Spec) { s.Architecture = "arm64" }, func(s *Spec) { s.CNI = "cilium" }, func(s *Spec) { s.Worker.NetworkMode = "evil" }, func(s *Spec) { s.SchematicID = "../x" }} {
		spec := fixtureSpec()
		mutate(&spec)
		if Validate(spec) == nil {
			t.Fatal("invalid spec accepted", spec)
		}
	}
	spec := fixtureSpec()
	spec.Worker.NetworkMode = "static"
	if e := Validate(spec); e != nil {
		t.Fatal("static defaults should validate with dummy addresses", e)
	}
}

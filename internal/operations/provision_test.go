package operations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"talosdeck/internal/clusters"
	"talosdeck/internal/proxmox"
)

func provisionFixture(t *testing.T) (*ProvisionService, ProvisionSpec, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := clusters.Open(filepath.Join(dir, "clusters.db"), filepath.Join(dir, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	provider, err := SaveProvider(context.Background(), store, "test", "proxmox", proxmox.Config{BaseURL: "https://provider.test:8006", Node: "pve", APIToken: "root@pam!test=private-provider-value", DefaultISO: "local:iso/talos.iso"}, false)
	if err != nil {
		t.Fatal(err)
	}
	spec := ProvisionSpec{Kind: "cluster-create", Name: "test-cluster", ProviderID: provider.ID, TalosVersion: "1.14.0", KubernetesVersion: "1.35.0", InstallerImage: "ghcr.io/siderolabs/installer:v1.14.0", Machines: []proxmox.MachineSpec{{Name: "test-cp", Role: "controlplane", Cores: 2, MemoryMB: 2048, DiskGB: 20, NetworkMode: "dhcp"}, {Name: "test-worker", Role: "worker", Cores: 2, MemoryMB: 2048, DiskGB: 20, NetworkMode: "static", Address: "10.20.0.20/24", Gateway: "10.20.0.1", Nameservers: []string{"1.1.1.1"}}}}
	return &ProvisionService{ClusterID: FleetScope, Store: store}, spec, dir
}
func TestGeneratedProvisionConfigsHaveSharedFreshCredentialsAndNetworking(t *testing.T) {
	for _, version := range []string{"1.13.10", "1.14.0"} {
		t.Run(version, func(t *testing.T) {
			s, spec, _ := provisionFixture(t)
			spec.TalosVersion = version
			spec.InstallerImage = "ghcr.io/siderolabs/installer:v" + version
			plan := &provisionState{ProvisionPlan: ProvisionPlan{ID: "plan", Spec: spec}}
			records := []proxmox.OwnedMachineRecord{}
			for i, machine := range spec.Machines {
				record := proxmox.NewOwnership(spec.ProviderID, FleetScope, "plan", "pve", machine, 150+i)
				record.Address = "10.20.0.10"
				records = append(records, record)
			}
			if err := s.generateConfigs(context.Background(), plan, records); err != nil {
				for _, data := range plan.Configs {
					if len(data) > 0 {
						provider, _ := configloader.NewFromBytes(data)
						_, validationErr := provider.ValidateAsClient(configRuntimeMode{})
						t.Logf("generated config validation: %v", validationErr)
					}
				}
				t.Fatal(err)
			}
			if len(plan.Configs) != 2 || len(plan.Talosconfig) == 0 {
				t.Fatal("credentials/configurations missing")
			}
			cp, err := configloader.NewFromBytes(plan.Configs[0])
			if err != nil {
				t.Fatal(err)
			}
			worker, err := configloader.NewFromBytes(plan.Configs[1])
			if err != nil {
				t.Fatal(err)
			}
			if cp.NetworkHostnameConfig().Hostname() != "test-cp" || worker.NetworkHostnameConfig().Hostname() != "test-worker" {
				t.Fatal("generated hostname mismatch")
			}
			if cp.DiscoveryIdentityConfig().ClusterID() != worker.DiscoveryIdentityConfig().ClusterID() {
				t.Fatal("machine configs belong to different clusters")
			}
			if !strings.Contains(string(plan.Configs[1]), "10.20.0.20/24") || !strings.Contains(string(plan.Configs[1]), records[1].MAC) {
				t.Fatal("static network not bound to VM MAC")
			}
		})
	}
}

type failCreateProvider struct {
	created   int
	deleted   int
	deleteErr error
}

func (f *failCreateProvider) NextID(context.Context) (int, error) { return 151, nil }
func (f *failCreateProvider) CreateMachine(context.Context, proxmox.MachineSpec, proxmox.OwnedMachine) (string, error) {
	f.created++
	return "", errors.New("network lost after create")
}
func (f *failCreateProvider) WaitTask(context.Context, string) error                  { return nil }
func (f *failCreateProvider) VerifyOwned(context.Context, proxmox.OwnedMachine) error { return nil }
func (f *failCreateProvider) StartOwned(context.Context, proxmox.OwnedMachine) error  { return nil }
func (f *failCreateProvider) MachineAddress(context.Context, proxmox.OwnedMachine) (string, error) {
	return "", nil
}
func (f *failCreateProvider) DeleteOwned(context.Context, proxmox.OwnedMachine) error {
	f.deleted++
	return f.deleteErr
}
func TestProvisionFailureRetainsOwnershipAndCannotReplay(t *testing.T) {
	s, spec, dir := provisionFixture(t)
	fake := &failCreateProvider{}
	s.ProviderFactory = func(ProviderRecord) (proxmox.MachineProvider, error) { return fake, nil }
	plan, err := s.Plan(context.Background(), spec, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteProvider(context.Background(), s.Store, spec.ProviderID); err == nil {
		t.Fatal("provider referenced by an unexpired plan deleted")
	}
	request, err := s.Request(context.Background(), plan.ID, spec.Name, "admin")
	if err != nil {
		t.Fatal(err)
	}
	job := runJob(t, &Service{Provision: s}, request)
	if len(job.Intents) != 1 || job.Intents[0].Action != "create" || job.Intents[0].Outcome != "UNKNOWN" || job.Intents[0].Evidence != nil {
		t.Fatalf("ambiguous create lacks durable unknown intent: %+v", job.Intents)
	}
	if job.Status != "interrupted" {
		t.Fatalf("%+v", job)
	}
	machines, err := ListOwnedMachines(context.Background(), s.Store, FleetScope)
	if err != nil || len(machines) != 1 || machines[0].Status != "reserved" {
		t.Fatalf("%+v %v", machines, err)
	}
	if _, err := s.Request(context.Background(), plan.ID, spec.Name, "admin"); err == nil {
		t.Fatal("partially executed plan replay allowed")
	}
	if fake.created != 1 {
		t.Fatal("unexpected retry")
	}
	views, err := ListProviders(context.Background(), s.Store)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(views)
	if strings.Contains(string(encoded), "private-provider-value") {
		t.Fatal("provider credential exposed")
	}
	for _, name := range []string{"clusters.db", "clusters.db-wal"} {
		raw, _ := os.ReadFile(filepath.Join(dir, name))
		if strings.Contains(string(raw), "private-provider-value") {
			t.Fatal("provider credential stored in plaintext")
		}
	}
	if err := DeleteProvider(context.Background(), s.Store, spec.ProviderID); err == nil {
		t.Fatal("provider with owned machine deleted")
	}
	if !machines[0].CleanupEligible {
		t.Fatal("failed unregistered resource lacks cleanup action")
	}
	retained, err := s.load(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	retained.Status = "running"
	if err := s.save(context.Background(), retained, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	retained, err = s.load(context.Background(), plan.ID)
	if err != nil || retained.Status != "interrupted" {
		t.Fatal("restart did not mark provisioning interrupted")
	}
	retained.ImportStarted = true
	if err := s.save(context.Background(), retained, false); err != nil {
		t.Fatal(err)
	}
	cleanupSpec := ProvisionSpec{Kind: "machine-cleanup", Name: machines[0].Name, MachineID: machines[0].ID, ProviderID: spec.ProviderID}
	if _, err := s.Plan(context.Background(), cleanupSpec, "admin"); err == nil {
		t.Fatal("uncertain registry import allowed control-plane cleanup")
	}
	retained.ImportStarted = false
	if err := s.save(context.Background(), retained, false); err != nil {
		t.Fatal(err)
	}
	cleanup, err := s.Plan(context.Background(), ProvisionSpec{Kind: "machine-cleanup", Name: machines[0].Name, MachineID: machines[0].ID, ProviderID: spec.ProviderID}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	cleanupRequest, err := s.Request(context.Background(), cleanup.ID, machines[0].Name, "admin")
	if err != nil {
		t.Fatal(err)
	}
	cleanupJob := runJob(t, &Service{Provision: s}, cleanupRequest)
	if len(cleanupJob.Intents) != 1 || cleanupJob.Intents[0].Action != "delete" || cleanupJob.Intents[0].Outcome != "succeeded" || cleanupJob.Intents[0].Evidence == nil {
		t.Fatalf("delete lacks provider completion proof: %+v", cleanupJob.Intents)
	}
	if cleanupJob.Status != "succeeded" {
		t.Fatalf("cleanup: %+v", cleanupJob)
	}
	machines, err = ListOwnedMachines(context.Background(), s.Store, FleetScope)
	if err != nil || machines[0].Status != "deleted" || machines[0].CleanupEligible {
		t.Fatalf("cleanup state: %+v %v", machines, err)
	}
}

func TestCleanupAmbiguousDeletePersistsUnknownAndDoesNotReplay(t *testing.T) {
	s, spec, _ := provisionFixture(t)
	p := &failCreateProvider{deleteErr: errors.New("response lost after delete")}
	s.ProviderFactory = func(ProviderRecord) (proxmox.MachineProvider, error) { return p, nil }
	plan, err := s.Plan(context.Background(), spec, "admin")
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.Request(context.Background(), plan.ID, spec.Name, "admin")
	if err != nil {
		t.Fatal(err)
	}
	runJob(t, &Service{Provision: s}, request)
	machines, err := ListOwnedMachines(context.Background(), s.Store, FleetScope)
	if err != nil || len(machines) != 1 {
		t.Fatal(machines, err)
	}
	cleanup, err := s.Plan(context.Background(), ProvisionSpec{Kind: "machine-cleanup", Name: machines[0].Name, MachineID: machines[0].ID, ProviderID: spec.ProviderID}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	request, err = s.Request(context.Background(), cleanup.ID, machines[0].Name, "admin")
	if err != nil {
		t.Fatal(err)
	}
	j := runJob(t, &Service{Provision: s}, request)
	if j.Status != "interrupted" || len(j.Intents) != 1 || j.Intents[0].Outcome != "UNKNOWN" || j.Intents[0].Evidence != nil || p.deleted != 1 {
		t.Fatalf("ambiguous delete incorrectly resolved: %+v calls=%d", j, p.deleted)
	}
	if _, err = s.Request(context.Background(), cleanup.ID, machines[0].Name, "admin"); err == nil {
		t.Fatal("ambiguous delete replayed")
	}
}

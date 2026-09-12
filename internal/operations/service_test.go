package operations

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"talosdeck/internal/backup"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/talos"
)

type fixture struct {
	mu          sync.Mutex
	nodes       []*talos.NodeOverview
	knodes      []k8s.UpgradeNode
	api         string
	commands    [][]string
	failBackup  bool
	failCommand bool
	image       string
}

func newFixture() *fixture {
	f := &fixture{api: "v1.34.0", image: "factory.talos.dev/metal-installer/abc:v1.13.0"}
	for i, ip := range []string{"10.0.0.10", "10.0.0.11", "10.0.0.12"} {
		role := "worker"
		if i == 0 {
			role = "controlplane"
		}
		f.nodes = append(f.nodes, &talos.NodeOverview{IP: ip, Hostname: ip, Role: role, Version: "v1.13.0", Ready: true, ServicesSummary: &talos.ServicesSummary{Kubelet: "Healthy", Apid: "Healthy", Containerd: "Healthy", Etcd: "Healthy"}})
		f.knodes = append(f.knodes, k8s.UpgradeNode{Name: ip, Addresses: []string{ip}, Version: "v1.34.0", Ready: true})
	}
	return f
}
func (f *fixture) ListNodes(context.Context) ([]*talos.NodeOverview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]*talos.NodeOverview, len(f.nodes))
	for i, n := range f.nodes {
		copy := *n
		result[i] = &copy
	}
	return result, nil
}
func (f *fixture) GetEtcdStatus(context.Context) (*talos.EtcdClusterStatus, error) {
	return &talos.EtcdClusterStatus{Healthy: true, Members: []talos.EtcdMemberInfo{{Healthy: true}}}, nil
}
func (f *fixture) GetInstallerImage(context.Context, string) (string, error) { return f.image, nil }
func (f *fixture) GetConfigPath() string                                     { return "/tmp/talos config" }
func (f *fixture) GetClusterName() string                                    { return "test-cluster" }
func (f *fixture) UpgradeInventory(context.Context) (string, []k8s.UpgradeNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.api, append([]k8s.UpgradeNode(nil), f.knodes...), nil
}
func (f *fixture) CreateEtcdSnapshot(context.Context, string) (*backup.BackupInfo, error) {
	if f.failBackup {
		return nil, errors.New("disk full")
	}
	return &backup.BackupInfo{ID: "snapshot-1"}, nil
}
func (f *fixture) VerifyBackup(string) (bool, string, error) { return true, "ok", nil }
func (f *fixture) Check(context.Context) error               { return nil }
func (f *fixture) Run(_ context.Context, args []string, log func(string) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, append([]string(nil), args...))
	if f.failCommand {
		return errors.New("command failed")
	}
	if err := log("updating components"); err != nil {
		return err
	}
	joined := " " + strings.Join(args, " ") + " "
	if strings.Contains(joined, " --dry-run ") {
		return nil
	}
	if strings.Contains(joined, " upgrade-k8s ") {
		target := args[len(args)-1]
		f.api = "v" + target
		for i := range f.knodes {
			f.knodes[i].Version = "v" + target
		}
	}
	if strings.Contains(joined, " upgrade ") {
		ip := args[5]
		for i, arg := range args {
			if arg == "--image" {
				image := args[i+1]
				v := image[strings.LastIndex(image, ":")+1:]
				for _, n := range f.nodes {
					if n.IP == ip {
						n.Version = v
					}
				}
			}
		}
	}
	return nil
}
func service(f *fixture) *Service {
	return &Service{Talos: f, Kubernetes: f, Backups: f, CLI: f, PollInterval: time.Millisecond}
}
func runJob(t *testing.T, s *Service, r jobs.Request) jobs.Job {
	t.Helper()
	m, err := jobs.Open(t.TempDir(), s.Run)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, err := m.Submit(r, "admin")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, _ = m.Get(j.ID)
		if j.Status != "running" && j.Status != "queued" {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job did not finish: %+v", j)
	return j
}
func TestTalosUpgradeIsSequentialAndPreservesSchematic(t *testing.T) {
	f := newFixture()
	j := runJob(t, service(f), jobs.Request{Kind: "talos-upgrade", Version: "1.14.0", AllowDowntime: true})
	if j.Status != "succeeded" {
		t.Fatalf("%s: %s", j.Status, j.Error)
	}
	if len(f.commands) != 3 {
		t.Fatalf("commands: %v", f.commands)
	}
	for i, args := range f.commands {
		if args[5] != f.nodes[i].IP || !strings.Contains(strings.Join(args, " "), "factory.talos.dev/metal-installer/abc:v1.14.0") {
			t.Fatalf("unexpected command: %v", args)
		}
	}
}
func TestKubernetesUsesOfficialDryRunThenUpgrade(t *testing.T) {
	f := newFixture()
	j := runJob(t, service(f), jobs.Request{Kind: "kubernetes-upgrade", Version: "1.35.0"})
	if j.Status != "succeeded" {
		t.Fatalf("%s", j.Error)
	}
	if len(f.commands) != 2 || f.commands[0][len(f.commands[0])-1] != "--dry-run" {
		t.Fatalf("commands: %v", f.commands)
	}
	want := []string{"--talosconfig", "/tmp/talos config", "--context", "test-cluster", "--nodes", "10.0.0.10", "upgrade-k8s", "--to", "1.35.0"}
	if !reflect.DeepEqual(f.commands[1], want) {
		t.Fatalf("%v", f.commands[1])
	}
}
func TestPreflightRefusesUnsafeRequests(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    string
		version string
		modify  func(*fixture)
	}{
		{"skip minor", "kubernetes-upgrade", "1.36.0", nil},
		{"downgrade", "talos-upgrade", "1.12.0", nil},
		{"unsupported future OS", "talos-upgrade", "1.15.0", nil},
		{"same version", "talos-upgrade", "1.13.0", nil},
		{"shell injection", "talos-upgrade", "1.14.0; touch /tmp/oops", nil},
		{"wrong kube context", "talos-upgrade", "1.14.0", func(f *fixture) { f.knodes[1].Addresses = []string{"192.0.2.1"} }},
		{"missing nodes", "talos-upgrade", "1.14.0", func(f *fixture) { f.nodes = f.nodes[:1] }},
		{"unhealthy worker", "talos-upgrade", "1.14.0", func(f *fixture) { f.knodes[1].Ready = false }},
		{"cordoned worker", "talos-upgrade", "1.14.0", func(f *fixture) { f.knodes[1].Unschedulable = true }},
		{"unknown installer", "talos-upgrade", "1.14.0", func(f *fixture) { f.image = "factory.talos.dev/installer/abc@sha256:abc" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			if tc.modify != nil {
				tc.modify(f)
			}
			j := runJob(t, service(f), jobs.Request{Kind: tc.kind, Version: tc.version, AllowDowntime: true})
			if j.Status == "succeeded" || len(f.commands) != 0 {
				t.Fatalf("unsafe request ran: %+v %v", j, f.commands)
			}
		})
	}
}
func TestSingleControlPlaneRequiresDowntimeAcknowledgement(t *testing.T) {
	_, err := service(newFixture()).Preflight(context.Background(), jobs.Request{Kind: "talos-upgrade", Version: "1.14.0"})
	if err == nil || !strings.Contains(err.Error(), "downtime") {
		t.Fatalf("%v", err)
	}
}
func TestBackupFailureAndCommandFailureStopFurtherNodes(t *testing.T) {
	for _, backupFailure := range []bool{true, false} {
		f := newFixture()
		f.failBackup = backupFailure
		f.failCommand = !backupFailure
		j := runJob(t, service(f), jobs.Request{Kind: "talos-upgrade", Version: "1.14.0", AllowDowntime: true})
		if j.Status != "failed" {
			t.Fatalf("%+v", j)
		}
		want := 1
		if backupFailure {
			want = 0
		}
		if len(f.commands) != want {
			t.Fatalf("executed %d commands after failure", len(f.commands))
		}
	}
}
func TestRollingRebootRunsWorkersFirstWithDrain(t *testing.T) {
	f := newFixture()
	j := runJob(t, service(f), jobs.Request{Kind: "rolling-reboot", AllowDowntime: true})
	if j.Status != "succeeded" {
		t.Fatal(j.Error)
	}
	if f.commands[0][5] != "10.0.0.11" || f.commands[2][5] != "10.0.0.10" || !strings.Contains(strings.Join(f.commands[0], " "), "--drain") {
		t.Fatalf("%v", f.commands)
	}
}

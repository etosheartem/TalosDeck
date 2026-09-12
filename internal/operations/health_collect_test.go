package operations

import (
	"context"
	"errors"
	"talosdeck/internal/health"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/talos"
	"testing"
	"time"
)

type healthFixture struct {
	fail  bool
	pods  []k8s.PodInfo
	etcd  *talos.EtcdClusterStatus
	disks []*talos.DiskInfo
}

func (f *healthFixture) ListNodes(context.Context) ([]*talos.NodeOverview, error) {
	if f.fail {
		return nil, errors.New("forbidden")
	}
	return []*talos.NodeOverview{{IP: "192.0.2.1", Role: "controlplane", Ready: true, MemoryUsageKnown: true, MemoryTotalBytes: 100}}, nil
}
func (f *healthFixture) ListServices(context.Context, string) ([]*talos.TalosService, error) {
	return []*talos.TalosService{{ID: "apid", State: "Running", Healthy: true, HealthKnown: true}}, nil
}
func (f *healthFixture) GetNodeDisks(context.Context, string) ([]*talos.DiskInfo, error) {
	return f.disks, nil
}
func (f *healthFixture) GetEtcdStatus(context.Context) (*talos.EtcdClusterStatus, error) {
	return f.etcd, nil
}
func (f *healthFixture) GetNodeConfig(context.Context, string) ([]byte, error) {
	return []byte("version: v1alpha1\ncluster:\n  network:\n    cni:\n      name: flannel\n"), nil
}
func (f *healthFixture) HealthReady(context.Context) error {
	if f.fail {
		return errors.New("forbidden")
	}
	return nil
}
func (f *healthFixture) UpgradeInventory(context.Context) (string, []k8s.UpgradeNode, error) {
	if f.fail {
		return "", nil, errors.New("forbidden")
	}
	return "v1.37.0", []k8s.UpgradeNode{{Name: "cp", Ready: true}}, nil
}
func (f *healthFixture) ListPods(context.Context, string, string) ([]k8s.PodInfo, error) {
	if f.fail {
		return nil, errors.New("forbidden")
	}
	return f.pods, nil
}
func (f *healthFixture) HealthNetworking(context.Context) ([]k8s.HealthWorkload, error) {
	if f.fail {
		return nil, errors.New("forbidden")
	}
	return []k8s.HealthWorkload{{Name: "kube-flannel", Namespace: "kube-flannel", Kind: "DaemonSet", Images: []string{"ghcr.io/flannel-io/flannel:v1"}, Ready: 1, Desired: 1}, {Name: "broken-unrelated", Namespace: "kube-system", Kind: "Deployment", Images: []string{"example/foo:v1"}, Desired: 1}, {Name: "coredns", Namespace: "kube-system", Kind: "Deployment", Images: []string{"registry.k8s.io/coredns/coredns:v1"}, Ready: 1, Desired: 1}}, nil
}
func (f *healthFixture) HealthClaims(context.Context) ([]k8s.HealthClaim, error) {
	if f.fail {
		return nil, errors.New("forbidden")
	}
	return nil, nil
}
func TestHealthUnknownSourcesCannotProducePerfectScore(t *testing.T) {
	f := &healthFixture{fail: true}
	s := (&HealthCollector{ClusterID: "test", Talos: f, Kubernetes: f}).Collect(context.Background())
	r := health.Evaluate(s, time.Now())
	if r.Score != nil || r.Coverage != 0 {
		t.Fatalf("unavailable source scored: %+v", r)
	}
	for _, c := range s.Checks {
		if c.State == "critical" {
			t.Fatalf("unavailable became actual critical: %+v", c)
		}
	}
}
func TestHealthCurrentEvidenceIgnoresLifetimeRestartsAndUnrelatedSystemWorkload(t *testing.T) {
	f := &healthFixture{etcd: &talos.EtcdClusterStatus{Healthy: true}, pods: []k8s.PodInfo{{Name: "healthy", Namespace: "default", Status: "Running", ReadyCount: 1, TotalContainers: 1, Restarts: 100}, {Name: "complete", Namespace: "default", Status: "Succeeded", Restarts: 50}}, disks: []*talos.DiskInfo{{Partitions: []talos.PartitionInfo{{ID: "STATE", MountPath: "/system/state", UsedPercent: 0}}}}}
	s := (&HealthCollector{Talos: f, Kubernetes: f}).Collect(context.Background())
	foundCNI, unknownDisk := false, false
	for _, c := range s.Checks {
		if c.Category == "workloads" && c.State != "healthy" {
			t.Fatalf("lifetime/completed penalized %+v", c)
		}
		if c.Category == "networking" && c.Reason == "cni-not-ready" {
			t.Fatal("unrelated system workload called CNI")
		}
		if c.Resource == "kube-flannel/kube-flannel" && c.State == "healthy" {
			foundCNI = true
		}
		if c.Category == "storage" && c.Reason == "unmeasured" {
			unknownDisk = true
		}
	}
	if !foundCNI || !unknownDisk {
		t.Fatalf("CNI/disk evidence incorrect %v %v", foundCNI, unknownDisk)
	}
}
func TestHealthPartialEtcdAndCustomCNIStayUnknown(t *testing.T) {
	f := &healthFixture{etcd: &talos.EtcdClusterStatus{Healthy: true, Errors: []string{"unavailable member"}}}
	s := (&HealthCollector{Talos: f, Kubernetes: f}).Collect(context.Background())
	for _, c := range s.Checks {
		if c.Category == "etcd" && c.State != "unknown" {
			t.Fatal("partial etcd became healthy")
		}
	}
	found := false
	collectNetworking([]k8s.HealthWorkload{{Kind: "DaemonSet", Images: []string{"custom/network:v1"}, Ready: 1, Desired: 1}}, "custom", func(cat, rule, res, node, state, reason, details string) {
		if rule == "cni" && state == "unknown" {
			found = true
		}
	})
	if !found {
		t.Fatal("custom CNI not unknown")
	}
}

type healthSnapshotFixture struct {
	value     health.Snapshot
	refreshed int
	err       error
}

func (f *healthSnapshotFixture) Snapshot(context.Context) health.Snapshot { return f.value }
func (f *healthSnapshotFixture) Refresh(context.Context) health.Snapshot {
	f.refreshed++
	return f.value
}
func TestDiagnosticsUsesNormalizedSharedHealthAndStableIDs(t *testing.T) {
	b := backupFixture(t)
	now := time.Now().UTC()
	snapshot := health.Snapshot{ID: "shared-snapshot", ClusterID: b.ClusterID, CheckedAt: now, Checks: []health.Check{{ID: "expired-check", Category: "certificates", State: "healthy", ObservedAt: now.Add(-time.Hour)}}}
	cached := &healthSnapshotFixture{value: snapshot}
	d := &DiagnosticsService{ClusterID: b.ClusterID, Store: b.Store, Health: cached}
	job := runJob(t, &Service{Diagnostics: d}, jobs.Request{Kind: "diagnostics"})
	if job.Status != "succeeded" {
		t.Fatalf("%+v", job)
	}
	report, err := d.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cached.refreshed != 1 || report.SnapshotID != snapshot.ID || report.Health == nil || report.Health.Score != nil {
		t.Fatal("diagnostics did not use shared normalized snapshot")
	}
	found := false
	for _, check := range report.Checks {
		if check.ID == "expired-check" {
			found = true
			if check.Severity == "critical" {
				t.Fatal("stale source called critical")
			}
		}
	}
	if !found {
		t.Fatal("normalized stale finding missing")
	}
	if _, err := d.Bundle(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (f *healthSnapshotFixture) HealthError() error { return f.err }
func TestDiagnosticsFailedRefreshDoesNotReportFreshSuccess(t *testing.T) {
	b := backupFixture(t)
	cached := &healthSnapshotFixture{value: health.Snapshot{ID: "old-snapshot", CheckedAt: time.Now().UTC(), Checks: []health.Check{}}, err: errors.New("persistence failed")}
	d := &DiagnosticsService{ClusterID: b.ClusterID, Store: b.Store, Health: cached}
	job := runJob(t, &Service{Diagnostics: d}, jobs.Request{Kind: "diagnostics"})
	if job.Status != "failed" {
		t.Fatalf("%s", job.Status)
	}
	report, err := d.Latest(context.Background())
	if err != nil || report.Status != "incomplete" || report.SnapshotID != "old-snapshot" {
		t.Fatalf("%+v %v", report, err)
	}
}

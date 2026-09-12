package operations

import (
	"talosdeck/internal/backup"
	"talosdeck/internal/k8s"
	"talosdeck/internal/talos"
	"testing"
	"time"
)

func TestRestoreHealthMustRemainStableAfterTransientReadiness(t *testing.T) {
	now, since := time.Now(), time.Time{}
	if restoreHealthStable(true, now, &since) || restoreHealthStable(true, now.Add(29*time.Second), &since) {
		t.Fatal("transient readiness accepted")
	}
	if restoreHealthStable(false, now.Add(30*time.Second), &since) || !since.IsZero() {
		t.Fatal("unhealthy observation did not reset stability window")
	}
	if restoreHealthStable(true, now.Add(31*time.Second), &since) || !restoreHealthStable(true, now.Add(61*time.Second), &since) {
		t.Fatal("continuous readiness window was not enforced")
	}
}

func TestRestoreRequiresCompleteOriginalControlPlaneSet(t *testing.T) {
	meta := &backup.FullBackupMetadata{Nodes: []backup.NodeMeta{{IP: "10.0.0.1", Role: "controlplane", Version: "v1.14.0"}, {IP: "10.0.0.2", Role: "controlplane", Version: "v1.14.0"}, {IP: "10.0.0.3", Role: "worker", Version: "v1.14.0"}}}
	exact := []RestoreNode{{IP: "10.0.0.1", Version: "1.14.0"}, {IP: "10.0.0.2", Version: "1.14.0"}}
	if !matchesRestoreInventory(meta, exact) {
		t.Fatal("matching full inventory refused")
	}
	for name, inventory := range map[string][]RestoreNode{"missing CP": exact[:1], "duplicate CP": {exact[0], exact[0]}, "wrong version": {exact[0], {IP: "10.0.0.2", Version: "1.13.0"}}, "worker substituted": {exact[0], {IP: "10.0.0.3", Version: "1.14.0"}}} {
		t.Run(name, func(t *testing.T) {
			if matchesRestoreInventory(meta, inventory) {
				t.Fatal("unsafe inventory accepted")
			}
		})
	}
}

func TestRestoreSuccessRequiresEveryPlannedControlPlaneAndEtcdIdentity(t *testing.T) {
	inventory := []RestoreNode{{IP: "10.0.0.1"}, {IP: "10.0.0.2"}}
	healthy := func() *talos.EtcdClusterStatus {
		return &talos.EtcdClusterStatus{Healthy: true, Members: []talos.EtcdMemberInfo{{ID: "one", Name: "cp1", PeerURLs: []string{"https://10.0.0.1:2380"}}, {ID: "two", Name: "cp2", PeerURLs: []string{"https://10.0.0.2:2380"}}}}
	}
	nodes := func() []k8s.UpgradeNode {
		return []k8s.UpgradeNode{{Name: "cp1", Addresses: []string{"10.0.0.1"}, Ready: true, ControlPlane: true}, {Name: "cp2", Addresses: []string{"10.0.0.2"}, Ready: true, ControlPlane: true}, {Name: "worker", Addresses: []string{"10.0.0.3"}, Ready: true}}
	}
	if !restoredControlPlanesReady(inventory, nodes(), healthy()) {
		t.Fatal("complete healthy restore refused")
	}
	for _, failure := range []string{"missing cp", "worker identity", "not ready", "missing etcd", "wrong etcd", "learner", "duplicate member"} {
		t.Run(failure, func(t *testing.T) {
			ns, etcd := nodes(), healthy()
			switch failure {
			case "missing cp":
				ns = append(ns[:1], ns[2])
			case "worker identity":
				ns[1].ControlPlane = false
			case "not ready":
				ns[1].Ready = false
			case "missing etcd":
				etcd.Members = etcd.Members[:1]
			case "wrong etcd":
				etcd.Members[1].Name = "other"
				etcd.Members[1].PeerURLs = []string{"https://10.0.0.9:2380"}
			case "learner":
				etcd.Members[1].IsLearner = true
			case "duplicate member":
				etcd.Members[1].ID = "one"
			}
			if restoredControlPlanesReady(inventory, ns, etcd) {
				t.Fatal("premature success")
			}
		})
	}
}

func TestPartialBackupDoesNotReplaceLastCompleteRestorePoint(t *testing.T) {
	older := &backup.BackupInfo{ID: "complete", Timestamp: time.Now().Add(-48 * time.Hour)}
	partial := &backup.BackupInfo{ID: "partial", Partial: true, Timestamp: time.Now()}
	if latestCompleteBackup([]*backup.BackupInfo{partial, older}) != older {
		t.Fatal("partial archive masked old complete backup")
	}
	if latestCompleteBackup([]*backup.BackupInfo{partial}) != nil {
		t.Fatal("partial archive reported complete")
	}
}

func TestDiagnosticPodStateDistinguishesCompletionFromReadiness(t *testing.T) {
	for _, tc := range []struct {
		pod  k8s.PodInfo
		want bool
	}{{k8s.PodInfo{Status: "Succeeded"}, false}, {k8s.PodInfo{Status: "Completed"}, false}, {k8s.PodInfo{Status: "Running", ReadyCount: 1, TotalContainers: 1}, false}, {k8s.PodInfo{Status: "Running", ReadyCount: 0, TotalContainers: 1}, true}, {k8s.PodInfo{Status: "CrashLoopBackOff"}, true}} {
		if podNeedsDiagnosticAttention(tc.pod) != tc.want {
			t.Fatalf("wrong diagnostic for %+v", tc.pod)
		}
	}
}

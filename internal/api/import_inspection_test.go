package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"talosdeck/internal/clusters"
	"talosdeck/internal/k8s"
	"talosdeck/internal/talos"
)

type inspectionTalos struct {
	nodes            []*talos.NodeOverview
	etcd             *talos.EtcdClusterStatus
	nodeErr, etcdErr error
	kubeconfig       []byte
	kubeErr          error
	kubeNode         string
}

func (f *inspectionTalos) ListNodes(context.Context) ([]*talos.NodeOverview, error) {
	return f.nodes, f.nodeErr
}
func (f *inspectionTalos) GetEtcdStatus(context.Context) (*talos.EtcdClusterStatus, error) {
	return f.etcd, f.etcdErr
}
func (f *inspectionTalos) GetKubernetesConfig(_ context.Context, node string) ([]byte, error) {
	f.kubeNode = node
	return f.kubeconfig, f.kubeErr
}

type inspectionKubernetes struct {
	version string
	nodes   []k8s.UpgradeNode
	err     error
}

func (f *inspectionKubernetes) UpgradeInventory(context.Context) (string, []k8s.UpgradeNode, error) {
	return f.version, f.nodes, f.err
}

func inspectionFixtures(t *testing.T) (*inspectionTalos, *inspectionKubernetes) {
	t.Helper()
	ca := newImportCA(t)
	kube := []byte(fmt.Sprintf("apiVersion: v1\nkind: Config\ncurrent-context: test\ncontexts:\n- name: test\n  context:\n    cluster: test\n    user: test\nclusters:\n- name: test\n  cluster:\n    server: https://kubernetes.example:6443\n    certificate-authority-data: %s\nusers:\n- name: test\n  user:\n    token: fixture-token\n", base64.StdEncoding.EncodeToString(ca.pem)))
	return &inspectionTalos{
		kubeconfig: kube,
		nodes: []*talos.NodeOverview{
			{IP: "10.0.0.1", Hostname: "cp-01", Role: "controlplane", Version: "v1.14.0", Ready: true},
			{IP: "10.0.0.2", Hostname: "worker-01", Role: "worker", Version: "v1.14.0", Ready: true},
		},
		etcd: &talos.EtcdClusterStatus{Healthy: true},
	}, &inspectionKubernetes{version: "v1.37.0", nodes: []k8s.UpgradeNode{
		{Name: "cp-01", Addresses: []string{"10.0.0.1"}, Ready: true},
		{Name: "worker-01", Addresses: []string{"10.0.0.2"}, Ready: true},
	}}
}

func TestImportInspectionIndependentClustersAndMetadata(t *testing.T) {
	first, second := clusters.Cluster{ID: "a", Name: "production"}, clusters.Cluster{ID: "b", Name: "stage"}
	tm, km := inspectionFixtures(t)
	if err := inspectImportedCluster(context.Background(), tm, km, &first, tm.kubeconfig); err != nil {
		t.Fatal(err)
	}
	otherTalos, otherKube := inspectionFixtures(t)
	otherTalos.nodes[1].Version = "v1.13.0"
	otherKube.version = "v1.36.0"
	if err := inspectImportedCluster(context.Background(), otherTalos, otherKube, &second, otherTalos.kubeconfig); err != nil {
		t.Fatal(err)
	}
	if first.ID != "a" || first.Name != "production" || first.Health != "healthy" || first.TalosVersion != "v1.14.0" || first.KubernetesVersion != "v1.37.0" {
		t.Fatalf("first metadata incorrect or overwritten: %+v", first)
	}
	if second.ID != "b" || second.Name != "stage" || second.Health != "healthy" || second.TalosVersion != "mixed" || second.KubernetesVersion != "v1.36.0" {
		t.Fatalf("second metadata: %+v", second)
	}
	if tm.kubeNode != "10.0.0.1" || otherTalos.kubeNode != "10.0.0.1" {
		t.Fatalf("Kubernetes identity not requested from control plane")
	}
}

func TestImportInspectionRejectsOverlappingNetworksWithDifferentCredentials(t *testing.T) {
	tm, km := inspectionFixtures(t)
	otherTalos, _ := inspectionFixtures(t)
	var cluster clusters.Cluster
	err := inspectImportedCluster(context.Background(), tm, km, &cluster, otherTalos.kubeconfig)
	if err == nil {
		t.Fatal("accepted Talos A with Kubernetes B despite different CA identities and identical node names/IPs")
	}
	if strings.Contains(err.Error(), "fixture-token") {
		t.Fatalf("credential identity failure leaked token: %v", err)
	}
	tm.kubeErr = errors.New("SECRET-KUBECONFIG-PROVIDER")
	err = inspectImportedCluster(context.Background(), tm, km, &cluster, tm.kubeconfig)
	if err == nil || strings.Contains(err.Error(), "SECRET-") {
		t.Fatalf("failed Talos-issued kubeconfig must fail closed and redact: %v", err)
	}
}

func TestImportInspectionRejectsPartialAndMismatchedDiscovery(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*inspectionTalos, *inspectionKubernetes)
		message string
	}{
		{"Talos unavailable", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.nodeErr = errors.New("SECRET-TALOS-CREDENTIAL") }, "Talos API unavailable"},
		{"empty Talos discovery", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.nodes = nil }, "Talos API unavailable"},
		{"Kubernetes unavailable", func(_ *inspectionTalos, km *inspectionKubernetes) {
			km.err = errors.New("SECRET-KUBERNETES-CREDENTIAL")
		}, "Kubernetes API unavailable"},
		{"different node count", func(_ *inspectionTalos, km *inspectionKubernetes) { km.nodes = km.nodes[:1] }, "discovery disagree"},
		{"wrong Kubernetes cluster", func(_ *inspectionTalos, km *inspectionKubernetes) { km.nodes[0].Addresses = []string{"192.0.2.10"} }, "node addresses do not match"},
		{"missing Talos version", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.nodes[1].Version = "" }, "discovery incomplete"},
		{"missing Talos node", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.nodes[1] = nil }, "discovery incomplete"},
		{"unidentified role", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.nodes[1].Role = "unknown" }, "discovery incomplete"},
		{"duplicate Talos node identity", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.nodes[1].IP = tm.nodes[0].IP }, "node addresses do not match"},
		{"ambiguous Kubernetes IP", func(_ *inspectionTalos, km *inspectionKubernetes) {
			km.nodes[1].Addresses = append(km.nodes[1].Addresses, km.nodes[0].Addresses[0])
		}, "ambiguous Kubernetes node identity"},
		{"workers without control plane", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.nodes[0].Role = "worker" }, "no Talos control plane"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm, km := inspectionFixtures(t)
			tc.change(tm, km)
			var cluster clusters.Cluster
			err := inspectImportedCluster(context.Background(), tm, km, &cluster, tm.kubeconfig)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("expected %q, got %v", tc.message, err)
			}
			if strings.Contains(err.Error(), "SECRET-") {
				t.Fatalf("SDK error leaked credentials: %v", err)
			}
		})
	}
}

func TestImportInspectionCanonicalNodeAddresses(t *testing.T) {
	tm, km := inspectionFixtures(t)
	tm.nodes[0].IP = "2001:db8::1"
	km.nodes[0].Addresses = []string{"2001:0db8:0:0:0:0:0:1"}
	km.nodes[1].Addresses = []string{"::ffff:10.0.0.2"}
	var cluster clusters.Cluster
	if err := inspectImportedCluster(context.Background(), tm, km, &cluster, tm.kubeconfig); err != nil {
		t.Fatalf("equivalent IPv6 and mapped IPv4 addresses rejected: %v", err)
	}
}

func TestImportInspectionPreservesDegradedClusterForTroubleshooting(t *testing.T) {
	cases := []struct {
		name   string
		change func(*inspectionTalos, *inspectionKubernetes)
	}{
		{"Talos NotReady", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.nodes[1].Ready = false }},
		{"Kubernetes NotReady", func(_ *inspectionTalos, km *inspectionKubernetes) { km.nodes[1].Ready = false }},
		{"etcd unhealthy", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.etcd.Healthy = false }},
		{"etcd missing", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.etcd = nil }},
		{"etcd API error", func(tm *inspectionTalos, _ *inspectionKubernetes) { tm.etcdErr = errors.New("connection refused") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tm, km := inspectionFixtures(t)
			tc.change(tm, km)
			var cluster clusters.Cluster
			if err := inspectImportedCluster(context.Background(), tm, km, &cluster, tm.kubeconfig); err != nil {
				t.Fatalf("recoverable degradation prevented import: %v", err)
			}
			if cluster.Health != "degraded" {
				t.Fatalf("false healthy import: %+v", cluster)
			}
		})
	}
}

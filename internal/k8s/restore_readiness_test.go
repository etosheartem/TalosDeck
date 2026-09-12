package k8s

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestRestoreReadinessLiveReadOnly(t *testing.T) {
	path := os.Getenv("TALOSDECK_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("set TALOSDECK_TEST_KUBECONFIG and TALOSDECK_TEST_NODES for opt-in read-only verification")
	}
	addresses := strings.Split(os.Getenv("TALOSDECK_TEST_NODES"), ",")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewK8sManagerFromBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := manager.VerifyRestoreReadiness(ctx, addresses, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreReadinessRequiresLiveAPIAndFreshBoundControlPlaneLeases(t *testing.T) {
	for _, failure := range []string{"", "api", "missing-node", "worker", "not-ready", "pressure", "stale-first", "stale-second", "future-lease", "missing-lease", "wrong-holder", "wrong-owner"} {
		t.Run(failure, func(t *testing.T) {
			after := time.Now().Add(-30 * time.Second)
			nodes := corev1.NodeList{}
			leases := map[string]coordinationv1.Lease{}
			for i, name := range []string{"cp-1", "cp-2"} {
				ip := []string{"10.42.0.10", "10.42.0.11"}[i]
				uid := types.UID("uid-" + name)
				nodes.Items = append(nodes.Items, corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: uid, Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}})
				renewed := metav1.NewMicroTime(time.Now().Add(-time.Second))
				holder := name
				leases[name] = coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: corev1.NamespaceNodeLease, OwnerReferences: []metav1.OwnerReference{{Kind: "Node", Name: name, UID: uid}}}, Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, RenewTime: &renewed}}
			}
			switch failure {
			case "missing-node":
				nodes.Items = nodes.Items[:1]
			case "worker":
				nodes.Items[1].Labels = nil
			case "not-ready":
				nodes.Items[1].Status.Conditions[0].Status = corev1.ConditionFalse
			case "pressure":
				nodes.Items[1].Status.Conditions = append(nodes.Items[1].Status.Conditions, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue})
			case "stale-first":
				l := leases["cp-1"]
				v := metav1.NewMicroTime(after.Add(-time.Minute))
				l.Spec.RenewTime = &v
				leases["cp-1"] = l
			case "stale-second":
				l := leases["cp-2"]
				v := metav1.NewMicroTime(after.Add(-time.Minute))
				l.Spec.RenewTime = &v
				leases["cp-2"] = l
			case "future-lease":
				l := leases["cp-2"]
				v := metav1.NewMicroTime(time.Now().Add(time.Hour))
				l.Spec.RenewTime = &v
				leases["cp-2"] = l
			case "missing-lease":
				delete(leases, "cp-2")
			case "wrong-holder":
				l := leases["cp-2"]
				v := "other-node"
				l.Spec.HolderIdentity = &v
				leases["cp-2"] = l
			case "wrong-owner":
				l := leases["cp-2"]
				l.OwnerReferences[0].UID = "replaced-node"
				leases["cp-2"] = l
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("readiness attempted mutation")
				}
				if r.URL.Path == "/readyz" {
					if failure == "api" {
						http.Error(w, "not ready", 503)
					} else {
						w.Write([]byte("ok"))
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/nodes" {
					json.NewEncoder(w).Encode(nodes)
					return
				}
				name := strings.TrimPrefix(r.URL.Path, "/apis/coordination.k8s.io/v1/namespaces/kube-node-lease/leases/")
				if lease, ok := leases[name]; ok {
					json.NewEncoder(w).Encode(lease)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			manager := &K8sManager{clientset: client}
			err = manager.VerifyRestoreReadiness(context.Background(), []string{"10.42.0.10", "10.42.0.11"}, after)
			if (err == nil) != (failure == "" || failure == "worker") {
				t.Fatalf("case %s: %v", failure, err)
			}
			if failure == "stale-second" {
				lease := leases["cp-2"]
				renewed := metav1.NewMicroTime(time.Now())
				lease.Spec.RenewTime = &renewed
				leases["cp-2"] = lease
				if err := manager.VerifyRestoreReadiness(context.Background(), []string{"10.42.0.10", "10.42.0.11"}, after); err != nil {
					t.Fatalf("fresh lease did not clear stale snapshot readiness: %v", err)
				}
			}
		})
	}
}

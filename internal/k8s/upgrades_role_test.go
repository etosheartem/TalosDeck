package k8s

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestUpgradeInventoryPreservesControlPlaneRole(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cp", Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}}}, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "master", Labels: map[string]string{"node-role.kubernetes.io/master": ""}}}, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}})
	mgr := &K8sManager{clientset: client}
	_, nodes, err := mgr.UpgradeInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.ControlPlane != (node.Name != "worker") {
			t.Fatalf("wrong role for %s", node.Name)
		}
	}
}

package k8s

import (
	"context"
	"errors"
	"talosdeck/internal/reconcile"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestDeleteProvisionedNodeRequiresIdentityAndUIDPrecondition(t *testing.T) {
	for _, address := range []string{"10.42.0.50", "10.42.0.51"} {
		t.Run(address, func(t *testing.T) {
			client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "original-worker"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.42.0.50"}}}})
			manager := &K8sManager{clientset: client}
			err := manager.DeleteProvisionedNode(context.Background(), "worker", address)
			if (err == nil) != (address == "10.42.0.50") {
				t.Fatalf("unexpected cleanup result: %v", err)
			}
			deletes := 0
			for _, action := range client.Actions() {
				if action.GetVerb() != "delete" {
					continue
				}
				deletes++
				options := action.(ktesting.DeleteAction).GetDeleteOptions()
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "original-worker" {
					t.Fatal("cleanup must target the verified UID")
				}
			}
			if address != "10.42.0.50" && deletes != 0 {
				t.Fatal("identity mismatch triggered deletion")
			}
		})
	}
}

func TestDeleteProvisionedNodeChecksAuthorityAfterIdentityRead(t *testing.T) {
	client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "original"}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}}}})
	manager := &K8sManager{clientset: client}
	ctx := reconcile.WithMutationGuard(context.Background(), func(context.Context) error { return reconcile.ErrAuthority })
	if err := manager.DeleteProvisionedNode(ctx, "worker", "10.0.0.1"); !errors.Is(err, reconcile.ErrAuthority) {
		t.Fatalf("expected refused mutation, got %v", err)
	}
	reads := 0
	for _, a := range client.Actions() {
		if a.GetVerb() == "delete" {
			t.Fatal("delete sent after authority loss")
		}
		if a.GetVerb() == "get" {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("identity observation must remain possible: %d", reads)
	}
}

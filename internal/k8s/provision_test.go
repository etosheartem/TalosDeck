package k8s

import (
	"context"
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

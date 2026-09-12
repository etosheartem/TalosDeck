package k8s

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeleteProvisionedNode removes only the node object matching the registered
// worker address; the UID precondition protects against same-name replacement.
func (m *K8sManager) DeleteProvisionedNode(ctx context.Context, name, address string) error {
	if m == nil || m.clientset == nil {
		return errors.New("Kubernetes client unavailable")
	}
	node, err := m.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return errors.New("cannot verify Kubernetes node before cleanup")
	}
	matched := false
	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP && a.Address == address {
			matched = true
		}
	}
	if !matched || node.UID == "" {
		return errors.New("Kubernetes node identity changed; cleanup refused")
	}
	uid := node.UID
	if err := m.clientset.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
		return errors.New("Kubernetes node cleanup failed")
	}
	return nil
}

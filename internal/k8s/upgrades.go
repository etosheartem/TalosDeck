package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type UpgradeNode struct {
	Name          string
	Addresses     []string
	Version       string
	Ready         bool
	Unschedulable bool
}

// UpgradeInventory bypasses UI caches. Internal addresses bind this Kubernetes
// context to the Talos machines before any destructive operation is allowed.
func (m *K8sManager) UpgradeInventory(ctx context.Context) (string, []UpgradeNode, error) {
	if m == nil || m.clientset == nil {
		return "", nil, fmt.Errorf("Kubernetes client unavailable")
	}
	nodes, err := m.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", nil, err
	}
	version, err := m.clientset.Discovery().ServerVersion()
	if err != nil {
		return "", nil, err
	}
	result := make([]UpgradeNode, 0, len(nodes.Items))
	for _, n := range nodes.Items {
		node := UpgradeNode{Name: n.Name, Version: n.Status.NodeInfo.KubeletVersion, Unschedulable: n.Spec.Unschedulable}
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				node.Addresses = append(node.Addresses, a.Address)
			}
		}
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				node.Ready = true
			}
		}
		result = append(result, node)
	}
	return version.GitVersion, result, nil
}

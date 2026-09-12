package k8s

import (
	"context"
	"fmt"
	"github.com/blang/semver/v4"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type UpgradeNode struct {
	Name          string
	Addresses     []string
	Version       string
	Ready         bool
	Unschedulable bool
	Pressure      bool
}

// VerifyUpgradeComponents checks the actual static pod images and readiness on
// every control-plane node, not just the API endpoint served by a load balancer.
// kube-proxy is checked when installed; clusters may replace it with their CNI.
func (m *K8sManager) VerifyUpgradeComponents(ctx context.Context, target string, exact bool) error {
	if m == nil || m.clientset == nil {
		return fmt.Errorf("Kubernetes client unavailable")
	}
	nodes, err := m.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	controlPlanes := map[string]bool{}
	for _, node := range nodes.Items {
		_, controlPlane := node.Labels["node-role.kubernetes.io/control-plane"]
		_, master := node.Labels["node-role.kubernetes.io/master"]
		if controlPlane || master {
			controlPlanes[node.Name] = true
		}
	}
	if len(controlPlanes) == 0 {
		return fmt.Errorf("no Kubernetes control-plane nodes found")
	}
	pods, err := m.clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	want, err := semver.Parse(strings.TrimPrefix(target, "v"))
	if err != nil {
		return fmt.Errorf("invalid component target version")
	}
	seen := map[string]bool{}
	for _, pod := range pods.Items {
		component := pod.Labels["component"]
		if component == "" && pod.Labels["k8s-app"] == "kube-proxy" {
			component = "kube-proxy"
		}
		if component != "kube-apiserver" && component != "kube-controller-manager" && component != "kube-scheduler" && component != "kube-proxy" {
			continue
		}
		if pod.DeletionTimestamp != nil {
			continue
		}
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready {
			return fmt.Errorf("Kubernetes component %s on %s is not ready", component, pod.Spec.NodeName)
		}
		found := false
		for _, container := range pod.Spec.Containers {
			if container.Name != component {
				continue
			}
			image := container.Image
			colon := strings.LastIndex(image, ":")
			if colon <= strings.LastIndex(image, "/") || strings.Contains(image, "@") {
				return fmt.Errorf("cannot verify Kubernetes component image version")
			}
			actual, e := semver.Parse(strings.TrimPrefix(image[colon+1:], "v"))
			if e != nil || actual.Major != want.Major || actual.Minor != want.Minor || (exact && actual.NE(want)) {
				return fmt.Errorf("Kubernetes component %s on %s does not match %s", component, pod.Spec.NodeName, target)
			}
			found = true
		}
		if !found {
			return fmt.Errorf("Kubernetes component container missing")
		}
		seen[pod.Spec.NodeName+"/"+component] = true
	}
	for name := range controlPlanes {
		for _, component := range []string{"kube-apiserver", "kube-controller-manager", "kube-scheduler"} {
			if !seen[name+"/"+component] {
				return fmt.Errorf("Kubernetes component %s missing on %s", component, name)
			}
		}
	}
	return nil
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
			if (c.Type == corev1.NodeDiskPressure || c.Type == corev1.NodeMemoryPressure || c.Type == corev1.NodePIDPressure) && c.Status != corev1.ConditionFalse {
				node.Pressure = true
			}
		}
		result = append(result, node)
	}
	return version.GitVersion, result, nil
}

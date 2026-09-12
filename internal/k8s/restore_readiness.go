package k8s

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VerifyRestoreReadiness bypasses UI caches and restored historical Ready flags.
// A fresh kubelet Lease proves each requested node has contacted the
// restored API after the destructive operation began. Callers should also check
// static components and require a sustained healthy interval.
func (m *K8sManager) VerifyRestoreReadiness(ctx context.Context, addresses []string, after time.Time) error {
	if m == nil || m.clientset == nil || m.clientset.Discovery().RESTClient() == nil {
		return fmt.Errorf("Kubernetes live readiness client unavailable")
	}
	if len(addresses) == 0 || after.IsZero() {
		return fmt.Errorf("restore node addresses and start time are required")
	}
	now := time.Now()
	if after.After(now.Add(5 * time.Second)) {
		return fmt.Errorf("restore start time is in the future")
	}
	response, err := m.clientset.Discovery().RESTClient().Get().AbsPath("/readyz").DoRaw(ctx)
	if err != nil || strings.TrimSpace(string(response)) != "ok" {
		return fmt.Errorf("Kubernetes API live readiness has not recovered")
	}
	nodes, err := m.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot read live Kubernetes nodes")
	}
	verified := map[string]bool{}
	for _, address := range addresses {
		want, err := netip.ParseAddr(address)
		if err != nil {
			return fmt.Errorf("restore target is not an IP address")
		}
		want = want.Unmap()
		var matched *corev1.Node
		for i := range nodes.Items {
			node := &nodes.Items[i]
			for _, a := range node.Status.Addresses {
				ip, e := netip.ParseAddr(a.Address)
				if a.Type != corev1.NodeInternalIP || e != nil || ip.Unmap() != want {
					continue
				}
				if matched != nil && matched.Name != node.Name {
					return fmt.Errorf("restore target matches multiple Kubernetes nodes")
				}
				matched = node
			}
		}
		if matched == nil {
			return fmt.Errorf("restored node %s is missing from Kubernetes", address)
		}
		if matched.UID == "" || matched.DeletionTimestamp != nil {
			return fmt.Errorf("restore target %s is not a current Kubernetes node", address)
		}
		if verified[matched.Name] {
			continue
		}
		ready := false
		for _, condition := range matched.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
			if (condition.Type == corev1.NodeMemoryPressure || condition.Type == corev1.NodeDiskPressure || condition.Type == corev1.NodePIDPressure) && condition.Status != corev1.ConditionFalse {
				return fmt.Errorf("restored node %s has resource pressure", matched.Name)
			}
		}
		if !ready {
			return fmt.Errorf("restored node %s is not Ready", matched.Name)
		}
		lease, err := m.clientset.CoordinationV1().Leases(corev1.NamespaceNodeLease).Get(ctx, matched.Name, metav1.GetOptions{})
		if err != nil || lease.Spec.RenewTime == nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != matched.Name {
			return fmt.Errorf("restored node %s has no valid kubelet lease", matched.Name)
		}
		bound := false
		for _, owner := range lease.OwnerReferences {
			if owner.Kind == "Node" && owner.Name == matched.Name && owner.UID == matched.UID {
				bound = true
			}
		}
		if !bound {
			return fmt.Errorf("kubelet lease does not match the restored node identity")
		}
		renewed := lease.Spec.RenewTime.Time
		leaseNow := time.Now()
		if !renewed.After(after) || renewed.After(leaseNow.Add(5*time.Second)) || leaseNow.Sub(renewed) > time.Minute {
			return fmt.Errorf("restored node %s has not renewed its kubelet lease after restore", matched.Name)
		}
		verified[matched.Name] = true
	}
	return nil
}

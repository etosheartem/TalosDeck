package k8s

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VerifyReplacementWorkloads is observation only. Unsupported owners and missing
// identities need operator review; a Ready replacement alone is not recovery.
// DaemonSets must have a Ready pod on the replacement. Changed placement rules
// that exclude it require review rather than silently weakening this condition.
func (m *K8sManager) VerifyReplacementWorkloads(ctx context.Context, impact ReplacementImpactReport, replacementNodeName string) error {
	if m == nil || m.clientset == nil || impact.Unknown || replacementNodeName == "" {
		return errors.New("workload recovery inventory unavailable")
	}
	seen := map[string]bool{}
	for _, affected := range impact.Pods {
		if affected.ControllerUID == "" || affected.ControllerName == "" || affected.Namespace == "" {
			return errors.New("affected workload has no pinned controller; review required")
		}
		key := affected.Namespace + "/" + affected.ControllerKind + "/" + affected.ControllerUID
		if seen[key] {
			continue
		}
		seen[key] = true
		var meta metav1.Object
		var desired, ready int32
		var observed int64
		switch affected.ControllerKind {
		case "ReplicaSet":
			c, err := m.clientset.AppsV1().ReplicaSets(affected.Namespace).Get(ctx, affected.ControllerName, metav1.GetOptions{})
			if err != nil {
				return errors.New("affected ReplicaSet unavailable")
			}
			meta = c
			desired = 1
			if c.Spec.Replicas != nil {
				desired = *c.Spec.Replicas
			}
			ready = c.Status.ReadyReplicas
			observed = c.Status.ObservedGeneration
			if parent := metav1.GetControllerOf(c); parent != nil {
				if parent.Kind != "Deployment" || parent.UID == "" {
					return errors.New("ReplicaSet parent requires review")
				}
				d, err := m.clientset.AppsV1().Deployments(affected.Namespace).Get(ctx, parent.Name, metav1.GetOptions{})
				if err != nil || d.UID != parent.UID || d.DeletionTimestamp != nil {
					return errors.New("Deployment identity unavailable")
				}
				n := int32(1)
				if d.Spec.Replicas != nil {
					n = *d.Spec.Replicas
				}
				if d.Status.ObservedGeneration < d.Generation || d.Status.ReadyReplicas < n || d.Status.AvailableReplicas < n || d.Status.UpdatedReplicas < n {
					return errors.New("affected Deployment has not recovered")
				}
			}
		case "Deployment":
			c, err := m.clientset.AppsV1().Deployments(affected.Namespace).Get(ctx, affected.ControllerName, metav1.GetOptions{})
			if err != nil {
				return errors.New("affected Deployment unavailable")
			}
			meta = c
			desired = 1
			if c.Spec.Replicas != nil {
				desired = *c.Spec.Replicas
			}
			ready = c.Status.ReadyReplicas
			observed = c.Status.ObservedGeneration
			if c.Status.AvailableReplicas < desired || c.Status.UpdatedReplicas < desired {
				return errors.New("affected Deployment has not recovered")
			}
		case "StatefulSet":
			c, err := m.clientset.AppsV1().StatefulSets(affected.Namespace).Get(ctx, affected.ControllerName, metav1.GetOptions{})
			if err != nil {
				return errors.New("affected StatefulSet unavailable")
			}
			meta = c
			desired = 1
			if c.Spec.Replicas != nil {
				desired = *c.Spec.Replicas
			}
			ready = c.Status.ReadyReplicas
			observed = c.Status.ObservedGeneration
		case "DaemonSet":
			c, err := m.clientset.AppsV1().DaemonSets(affected.Namespace).Get(ctx, affected.ControllerName, metav1.GetOptions{})
			if err != nil {
				return errors.New("affected DaemonSet unavailable")
			}
			meta = c
			desired = c.Status.DesiredNumberScheduled
			ready = c.Status.NumberReady
			observed = c.Status.ObservedGeneration
			if c.Status.NumberUnavailable != 0 || c.Status.UpdatedNumberScheduled < desired {
				return errors.New("affected DaemonSet has not recovered")
			}
		default:
			return errors.New("affected controller kind requires review")
		}
		if string(meta.GetUID()) != affected.ControllerUID || meta.GetDeletionTimestamp() != nil {
			return errors.New("affected controller identity changed")
		}
		if observed < meta.GetGeneration() || ready < desired {
			return fmt.Errorf("affected %s has not recovered", affected.ControllerKind)
		}
		// List the namespace, then bind by owner UID rather than labels (which can
		// select an unrelated workload). Pagination and bounded inventory fail closed.
		token := ""
		count := 0
		healthy := int32(0)
		replacementReady := false
		for {
			list, err := m.clientset.CoreV1().Pods(affected.Namespace).List(ctx, metav1.ListOptions{Limit: 500, Continue: token})
			if err != nil {
				return errors.New("affected pod inventory unavailable")
			}
			count += len(list.Items)
			if count > 10000 {
				return errors.New("affected pod inventory limit exceeded")
			}
			for _, pod := range list.Items {
				owner := metav1.GetControllerOf(&pod)
				if owner == nil || string(owner.UID) != affected.ControllerUID {
					continue
				}
				if pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" || pod.Spec.NodeName == impact.NodeName || pod.Status.Phase != corev1.PodRunning {
					return errors.New("affected pod is not recovered")
				}
				podReady := false
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						podReady = true
					}
				}
				if !podReady {
					return errors.New("affected pod is not Ready")
				}
				healthy++
				if pod.Spec.NodeName == replacementNodeName {
					replacementReady = true
				}
			}
			if list.Continue == "" {
				break
			}
			if list.Continue == token {
				return errors.New("affected pod pagination stalled")
			}
			token = list.Continue
		}
		if healthy < desired || affected.ControllerKind == "DaemonSet" && !replacementReady {
			return errors.New("affected controller lacks verified Ready pods")
		}
	}
	return nil
}

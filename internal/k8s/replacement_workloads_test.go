package k8s

import (
	"context"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func recoveredPod(kind, uid, node string) *corev1.Pod {
	yes := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "recovered", Namespace: "app", UID: "pod-new", OwnerReferences: []metav1.OwnerReference{{Kind: kind, Name: "workload", UID: types.UID(uid), Controller: &yes}}}, Spec: corev1.PodSpec{NodeName: node}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
}
func TestReplacementWorkloadRecoveryRejectsFalsePositive(t *testing.T) {
	for _, scenario := range []string{"healthy", "reused-controller", "stale-generation", "unscheduled", "failed", "unready", "old-node", "wrong-owner", "missing-identity", "no-controller", "status-without-pods"} {
		t.Run(scenario, func(t *testing.T) {
			one := int32(1)
			rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "app", UID: "rs-original", Generation: 2}, Spec: appsv1.ReplicaSetSpec{Replicas: &one}, Status: appsv1.ReplicaSetStatus{ObservedGeneration: 2, ReadyReplicas: 1}}
			pod := recoveredPod("ReplicaSet", "rs-original", "new-worker")
			affected := ReplacementPodImpact{Namespace: "app", ControllerKind: "ReplicaSet", ControllerName: "workload", ControllerUID: "rs-original"}
			switch scenario {
			case "reused-controller":
				rs.UID = "rs-new"
			case "stale-generation":
				rs.Status.ObservedGeneration = 1
			case "unscheduled":
				pod.Spec.NodeName = ""
			case "failed":
				pod.Status.Phase = corev1.PodFailed
			case "unready":
				pod.Status.Conditions = nil
			case "old-node":
				pod.Spec.NodeName = "old-worker"
			case "wrong-owner":
				pod.OwnerReferences[0].UID = "other"
			case "missing-identity":
				affected.ControllerUID = ""
			case "no-controller":
				affected.ControllerKind = ""
			}
			c := fake.NewClientset(rs, pod)
			if scenario == "status-without-pods" {
				if err := c.CoreV1().Pods("app").Delete(context.Background(), pod.Name, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			c.ClearActions()
			err := (&K8sManager{clientset: c}).VerifyReplacementWorkloads(context.Background(), ReplacementImpactReport{NodeName: "old-worker", Pods: []ReplacementPodImpact{affected}}, "new-worker")
			if (err == nil) != (scenario == "healthy") {
				t.Fatalf("unexpected result %v", err)
			}
			for _, a := range c.Actions() {
				if a.GetVerb() != "get" && a.GetVerb() != "list" {
					t.Fatalf("verification mutated %v", a)
				}
			}
		})
	}
}
func TestReplacementDaemonSetRequiresReplacementPod(t *testing.T) {
	for _, node := range []string{"other-worker", "new-worker"} {
		t.Run(node, func(t *testing.T) {
			ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "app", UID: "ds", Generation: 1}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 1, NumberReady: 1, UpdatedNumberScheduled: 1}}
			c := fake.NewClientset(ds, recoveredPod("DaemonSet", "ds", node))
			err := (&K8sManager{clientset: c}).VerifyReplacementWorkloads(context.Background(), ReplacementImpactReport{NodeName: "old", Pods: []ReplacementPodImpact{{Namespace: "app", ControllerKind: "DaemonSet", ControllerName: "workload", ControllerUID: "ds"}}}, "new-worker")
			if (err == nil) != (node == "new-worker") {
				t.Fatalf("unexpected result %v", err)
			}
		})
	}
}

func TestReplacementReplicaSetCannotHideUnhealthyDeployment(t *testing.T) {
	one := int32(1)
	yes := true
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "app", UID: "rs", Generation: 1, OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "parent", UID: "deployment", Controller: &yes}}}, Spec: appsv1.ReplicaSetSpec{Replicas: &one}, Status: appsv1.ReplicaSetStatus{ObservedGeneration: 1, ReadyReplicas: 1}}
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: "app", UID: "deployment", Generation: 2}, Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1}}
	c := fake.NewClientset(rs, d, recoveredPod("ReplicaSet", "rs", "new"))
	m := &K8sManager{clientset: c}
	impact := ReplacementImpactReport{NodeName: "old", Pods: []ReplacementPodImpact{{Namespace: "app", ControllerKind: "ReplicaSet", ControllerName: "workload", ControllerUID: "rs"}}}
	if err := m.VerifyReplacementWorkloads(context.Background(), impact, "new"); err == nil {
		t.Fatal("stale Deployment accepted")
	}
	d.Status.ObservedGeneration = 2
	if _, err := c.AppsV1().Deployments("app").UpdateStatus(context.Background(), d, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyReplacementWorkloads(context.Background(), impact, "new"); err != nil {
		t.Fatal(err)
	}
}

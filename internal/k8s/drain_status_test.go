package k8s

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func ptrBool(b bool) *bool { return &b }

// K8S-13: only a reference with Controller=true guarantees recreation.
func TestDrain_RejectsPodWithNonControllerOwner(t *testing.T) {
	fakeClient := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "orphan", Namespace: "default",
				// Owner reference present, but NOT a controller.
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rs", Controller: ptrBool(false)}},
			},
			Spec: corev1.PodSpec{NodeName: "w1"},
		},
	)

	mgr := &K8sManager{clientset: fakeClient}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := mgr.CordonAndDrainNode(ctx, "w1")
	if err == nil {
		t.Fatal("expected drain to refuse a pod whose owner reference is not a controller")
	}
}

// K8S-13: a non-controller DaemonSet reference must not exempt a workload pod.
func TestSkipPodDuringDrain_OnlyControllerDaemonSetIsSkipped(t *testing.T) {
	real := corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "ds", Controller: ptrBool(true)}},
	}}
	if !skipPodDuringDrain(real) {
		t.Error("a DaemonSet-controlled pod must be skipped")
	}

	fake := corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "ds", Controller: ptrBool(false)}},
	}}
	if skipPodDuringDrain(fake) {
		t.Error("a plain (non-controller) DaemonSet reference must not exempt a pod from drain")
	}
}

// K8S-14: emptyDir data must not be destroyed without an explicit opt-in.
func TestDrain_RefusesEmptyDirUnlessAllowed(t *testing.T) {
	newClient := func() *fake.Clientset {
		return fake.NewSimpleClientset(
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cache", Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rs", Controller: ptrBool(true)}},
				},
				Spec: corev1.PodSpec{
					NodeName: "w1",
					Volumes:  []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				},
			},
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mgr := &K8sManager{clientset: newClient()}
	if err := mgr.CordonAndDrainNode(ctx, "w1"); err == nil {
		t.Fatal("expected drain to refuse a pod with emptyDir data by default")
	}

	allowed := newClient()
	allowed.PrependReactor("create", "pods", evictionReactor(allowed, nil))
	mgrAllowed := &K8sManager{clientset: allowed, allowEmptyDirDeletion: true}
	if err := mgrAllowed.CordonAndDrainNode(ctx, "w1"); err != nil {
		t.Fatalf("drain should proceed once emptyDir deletion is allowed: %v", err)
	}
}

// K8S-15: the pod's own termination grace period must be honoured.
func TestDrain_HonoursPodTerminationGracePeriod(t *testing.T) {
	grace := int64(120)
	fakeClient := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "db", Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "sts", Controller: ptrBool(true)}},
			},
			Spec: corev1.PodSpec{NodeName: "w1", TerminationGracePeriodSeconds: &grace},
		},
	)

	var seen *int64
	fakeClient.PrependReactor("create", "pods", evictionReactor(fakeClient, func(e *policyv1.Eviction) {
		if e.DeleteOptions != nil {
			seen = e.DeleteOptions.GracePeriodSeconds
		}
	}))

	mgr := &K8sManager{clientset: fakeClient}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := mgr.CordonAndDrainNode(ctx, "w1"); err != nil {
		t.Fatalf("drain failed: %v", err)
	}

	if seen == nil || *seen != 120 {
		t.Fatalf("expected the pod's own 120s grace period to be used, got %v", seen)
	}
}

// K8S-16: a transient PDB rejection must be retried, not fatal.
func TestDrain_RetriesWhileDisruptionBudgetBlocks(t *testing.T) {
	fakeClient := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "app", Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rs", Controller: ptrBool(true)}},
			},
			Spec: corev1.PodSpec{NodeName: "w1"},
		},
	)

	attempts := 0
	fakeClient.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		attempts++
		if attempts == 1 {
			// First attempt blocked by a PodDisruptionBudget.
			return true, nil, apierrors.NewTooManyRequests("disruption budget", 1)
		}
		create := action.(k8stesting.CreateAction)
		eviction := create.GetObject().(*policyv1.Eviction)
		_ = fakeClient.Tracker().Delete(
			schema.GroupVersionResource{Version: "v1", Resource: "pods"},
			eviction.Namespace, eviction.Name)
		return true, eviction, nil
	})

	mgr := &K8sManager{clientset: fakeClient}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := mgr.CordonAndDrainNode(ctx, "w1"); err != nil {
		t.Fatalf("drain should survive a transient PDB rejection: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("expected the eviction to be retried, got %d attempt(s)", attempts)
	}
}

// K8S-19/K8S-20: surface the real reason and the most severe container state.
func TestPodDisplayStatus(t *testing.T) {
	t.Run("evicted pod surfaces its reason", func(t *testing.T) {
		p := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}}
		if got := podDisplayStatus(p); got != "Evicted" {
			t.Errorf("expected Evicted, got %q", got)
		}
	})

	t.Run("unschedulable pod surfaces the condition reason", func(t *testing.T) {
		p := &corev1.Pod{Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
			}},
		}}
		if got := podDisplayStatus(p); got != "Unschedulable" {
			t.Errorf("expected Unschedulable, got %q", got)
		}
	})

	t.Run("a completed first container must not mask a crashing sibling", func(t *testing.T) {
		p := &corev1.Pod{Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}},
				{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
			},
		}}
		if got := podDisplayStatus(p); got != "CrashLoopBackOff" {
			t.Errorf("expected CrashLoopBackOff to win over Completed, got %q", got)
		}
	})

	t.Run("terminating wins outright", func(t *testing.T) {
		now := metav1.Now()
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &now},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
		if got := podDisplayStatus(p); got != "Terminating" {
			t.Errorf("expected Terminating, got %q", got)
		}
	})
}

// evictionReactor deletes the evicted pod so drain's wait loop can finish.
func evictionReactor(c *fake.Clientset, observe func(*policyv1.Eviction)) k8stesting.ReactionFunc {
	return func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		create, ok := action.(k8stesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		eviction, ok := create.GetObject().(*policyv1.Eviction)
		if !ok {
			return false, nil, nil
		}
		if observe != nil {
			observe(eviction)
		}
		_ = c.Tracker().Delete(
			schema.GroupVersionResource{Version: "v1", Resource: "pods"},
			eviction.Namespace, eviction.Name)
		return true, eviction, nil
	}
}

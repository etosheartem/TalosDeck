package k8s

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestK8sManager_NilSafety(t *testing.T) {
	var mgr *K8sManager
	_, err := mgr.ListPods(context.Background(), "", "")
	if err == nil {
		t.Errorf("expected error on nil manager, got nil")
	}

	_, err = mgr.ListNamespaces(context.Background())
	if err == nil {
		t.Errorf("expected error on nil manager, got nil")
	}
}

func TestK8sManager_ListPods_FilteringAndStatus(t *testing.T) {
	fakeClient := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pod-running",
				Namespace:         "default",
				UID:               "uid-1",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
			},
			Spec: corev1.PodSpec{
				NodeName: "worker-1",
				Containers: []corev1.Container{
					{Name: "app"},
				},
			},
			Status: corev1.PodStatus{
				Phase:  corev1.PodRunning,
				HostIP: "10.42.0.111",
				PodIP:  "10.244.1.5",
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:         "app",
						Ready:        true,
						RestartCount: 2,
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{},
						},
					},
				},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pod-oomkilled",
				Namespace:         "default",
				UID:               "uid-2",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
			},
			Spec: corev1.PodSpec{
				NodeName: "worker-2",
				Containers: []corev1.Container{
					{Name: "worker"},
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:         "worker",
						Ready:        false,
						RestartCount: 5,
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 137,
								Reason:   "OOMKilled",
							},
						},
					},
				},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "pod-init-failure",
				Namespace:         "kube-system",
				UID:               "uid-3",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Minute)),
			},
			Spec: corev1.PodSpec{
				NodeName: "worker-1",
				InitContainers: []corev1.Container{
					{Name: "init-db"},
				},
				Containers: []corev1.Container{
					{Name: "server"},
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				InitContainerStatuses: []corev1.ContainerStatus{
					{
						Name:         "init-db",
						RestartCount: 3,
						State: corev1.ContainerState{
							Waiting: &corev1.ContainerStateWaiting{
								Reason: "CrashLoopBackOff",
							},
						},
					},
				},
			},
		},
	)

	mgr := &K8sManager{
		clientset: fakeClient,
		cache: podCache{
			entries: make(map[string]podCacheEntry),
			ttl:     1 * time.Second,
		},
	}

	ctx := context.Background()

	// 1. Test "all" filter returns all pods
	allPods, err := mgr.ListPods(ctx, "all", "all")
	if err != nil {
		t.Fatalf("ListPods failed: %v", err)
	}
	if len(allPods) != 3 {
		t.Errorf("expected 3 pods for all/all, got %d", len(allPods))
	}

	// 2. Verify DTO compatibility fields
	pod1 := allPods[0]
	if pod1.ID == "" {
		t.Errorf("expected non-empty pod ID")
	}
	if pod1.NodeName == "" || pod1.Node == "" {
		t.Errorf("expected nodeName and node to be set")
	}
	if pod1.ReadyContainers != "1/1" && pod1.ReadyContainers != "0/1" {
		t.Errorf("expected readyContainers format 'X/Y', got %s", pod1.ReadyContainers)
	}

	// 3. Test OOMKilled status detection
	var oomPod *PodInfo
	for _, p := range allPods {
		if p.Name == "pod-oomkilled" {
			oomPod = &p
			break
		}
	}
	if oomPod == nil {
		t.Fatalf("pod-oomkilled not found")
	}
	if oomPod.Status != "OOMKilled" {
		t.Errorf("expected status OOMKilled, got %s", oomPod.Status)
	}

	// 4. Test Init container status detection and restart count
	var initPod *PodInfo
	for _, p := range allPods {
		if p.Name == "pod-init-failure" {
			initPod = &p
			break
		}
	}
	if initPod == nil {
		t.Fatalf("pod-init-failure not found")
	}
	if initPod.Status != "Init:CrashLoopBackOff" {
		t.Errorf("expected status Init:CrashLoopBackOff, got %s", initPod.Status)
	}
	if initPod.Restarts != 3 {
		t.Errorf("expected 3 restarts from init container, got %d", initPod.Restarts)
	}

	// 5. Test caching behavior
	cachedPods, err := mgr.ListPods(ctx, "all", "all")
	if err != nil {
		t.Fatalf("cached ListPods failed: %v", err)
	}
	if len(cachedPods) != len(allPods) {
		t.Errorf("expected same length from cache")
	}

	if _, err := mgr.ListPods(ctx, "default", "all"); err != nil {
		t.Fatalf("filtered ListPods failed: %v", err)
	}
	if _, err := mgr.ListPods(ctx, "all", "all"); err != nil {
		t.Fatalf("second cached ListPods failed: %v", err)
	}
	listActions := 0
	for _, action := range fakeClient.Actions() {
		if action.GetVerb() == "list" && action.GetResource().Resource == "pods" {
			listActions++
		}
	}
	if listActions != 2 {
		t.Fatalf("expected two API list calls for two cache keys, got %d", listActions)
	}
}

func TestK8sManager_ListNamespaces(t *testing.T) {
	fakeClient := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
			},
			Status: corev1.NamespaceStatus{
				Phase: corev1.NamespaceActive,
			},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kube-system",
			},
			Status: corev1.NamespaceStatus{
				Phase: corev1.NamespaceActive,
			},
		},
	)

	mgr := &K8sManager{clientset: fakeClient}
	nsList, err := mgr.ListNamespaces(context.Background())
	if err != nil {
		t.Fatalf("ListNamespaces failed: %v", err)
	}
	if len(nsList) != 2 {
		t.Errorf("expected 2 namespaces, got %d", len(nsList))
	}
}

func TestSetNodeMaintenanceResolvesIP(t *testing.T) {
	fakeClient := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "talos-worker-1"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: "10.42.0.111"},
		}},
	})
	mgr := &K8sManager{clientset: fakeClient}

	name, err := mgr.SetNodeMaintenance(context.Background(), "10.42.0.111", true)
	if err != nil {
		t.Fatalf("SetNodeMaintenance failed: %v", err)
	}
	if name != "talos-worker-1" {
		t.Fatalf("expected resolved node name, got %q", name)
	}
	node, err := fakeClient.CoreV1().Nodes().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to read patched node: %v", err)
	}
	if !node.Spec.Unschedulable {
		t.Fatal("expected node to be cordoned")
	}
}

func TestCordonAndDrainUsesEvictionAndWaits(t *testing.T) {
	controller := true
	fakeClient := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "talos-worker-1"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "app", Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "app-rs", Controller: &controller}},
			},
			Spec: corev1.PodSpec{NodeName: "talos-worker-1"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "network-agent", Namespace: "kube-system",
				OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Name: "network-agent", Controller: &controller}},
			},
			Spec: corev1.PodSpec{NodeName: "talos-worker-1"},
		},
	)
	evictions := 0
	fakeClient.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateAction)
		if !ok || action.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		eviction, ok := create.GetObject().(*policyv1.Eviction)
		if !ok {
			return true, nil, fmt.Errorf("unexpected eviction object %T", create.GetObject())
		}
		evictions++
		if err := fakeClient.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), eviction.Namespace, eviction.Name); err != nil {
			return true, nil, err
		}
		return true, eviction, nil
	})

	mgr := &K8sManager{clientset: fakeClient}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := mgr.CordonAndDrainNode(ctx, "talos-worker-1"); err != nil {
		t.Fatalf("CordonAndDrainNode failed: %v", err)
	}
	if evictions != 1 {
		t.Fatalf("expected one workload eviction, got %d", evictions)
	}
	if _, err := fakeClient.CoreV1().Pods("kube-system").Get(ctx, "network-agent", metav1.GetOptions{}); err != nil {
		t.Fatalf("daemonset pod should remain on the node: %v", err)
	}
}

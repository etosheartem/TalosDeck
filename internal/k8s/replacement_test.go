package k8s

import (
	"context"
	"encoding/json"
	"errors"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"talosdeck/internal/reconcile"
	"testing"
)

func replacementNode() *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "worker-uid", ResourceVersion: "42"}}
}
func TestStaleNodeDeletePinsUIDAndRejectsControlPlane(t *testing.T) {
	for _, tc := range []struct {
		name    string
		node    *corev1.Node
		uid     string
		allowed bool
	}{{"worker", replacementNode(), "worker-uid", true}, {"recreated", replacementNode(), "different", false}, {"no-identity", replacementNode(), "", false}, {"controlplane", &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "worker-uid", Labels: map[string]string{"node-role.kubernetes.io/control-plane": "", "node-role.kubernetes.io/worker": ""}}}, "worker-uid", false}, {"legacy-role", &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "worker-uid", Labels: map[string]string{"kubernetes.io/role": "master"}}}, "worker-uid", false}, {"tainted", &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "worker-uid"}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: "node-role.kubernetes.io/control-plane"}}}}, "worker-uid", false}} {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientset(tc.node)
			deleted := false
			c.PrependReactor("delete", "nodes", func(a ktesting.Action) (bool, runtime.Object, error) {
				deleted = true
				p := a.(ktesting.DeleteAction).GetDeleteOptions().Preconditions
				if p == nil || p.UID == nil || string(*p.UID) != "worker-uid" || p.ResourceVersion == nil || *p.ResourceVersion != "42" {
					t.Fatal("delete not protected by pinned identity and role observation version")
				}
				return true, nil, nil
			})
			m := &K8sManager{clientset: c}
			e := m.DeleteStaleNode(reconcile.WithMutationGuard(context.Background(), func(context.Context) error { return nil }), "worker", tc.uid)
			if tc.allowed != (e == nil) || deleted != tc.allowed {
				t.Fatalf("deleted=%v err=%v", deleted, e)
			}
		})
	}
}
func TestReplacementImpactPersistentDataAndUnknownInventory(t *testing.T) {
	sc := "local-path"
	controller := true
	c := fake.NewClientset(replacementNode(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: "prod", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{{Name: "database", Kind: "StatefulSet", Controller: &controller}}}, Spec: corev1.PodSpec{NodeName: "worker", Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}, {Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{}}}}}}, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "prod", UID: "pvc-uid"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-data", StorageClassName: &sc}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-data", UID: "pv-uid"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: sc, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete, ClaimRef: &corev1.ObjectReference{Name: "data", Namespace: "prod", UID: "pvc-uid"}, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, PersistentVolumeSource: corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/local/data"}}, NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{}}}}, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: sc}, Provisioner: "rancher.io/local-path"})
	m := &K8sManager{clientset: c}
	r, e := m.ReplacementImpact(context.Background(), "worker", "worker-uid")
	if e != nil {
		t.Fatal(e)
	}
	if r.Unknown || !r.RequiresReview || len(r.Volumes) != 3 {
		t.Fatalf("bad impact %+v", r)
	}
	var local bool
	for _, v := range r.Volumes {
		if v.Kind == "hostPath-pv" {
			local = true
			if v.State != "requires_review" || v.ReclaimPolicy != "Delete" || v.LocalPath == "" || v.NodeAffinity == nil || v.PVCUID != "pvc-uid" {
				t.Fatalf("missing storage evidence %+v", v)
			}
		}
		if v.Kind == "api-projected" && v.State != "portable" {
			t.Fatal("projected data marked persistent")
		}
	}
	if !local {
		t.Fatal("local PV absent")
	}
	c.PrependReactor("list", "volumeattachments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("provider unavailable")
	})
	r, e = m.ReplacementImpact(context.Background(), "worker", "worker-uid")
	if e != nil || !r.Unknown {
		t.Fatalf("missing attachment inventory not unknown %+v %v", r, e)
	}
}
func TestReplacementImpactMissingPVCAndUIDChange(t *testing.T) {
	c := fake.NewClientset(replacementNode(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "prod"}, Spec: corev1.PodSpec{NodeName: "worker", Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "missing"}}}}}})
	m := &K8sManager{clientset: c}
	r, e := m.ReplacementImpact(context.Background(), "worker", "worker-uid")
	if e != nil || !r.Unknown || r.Volumes[0].State != "unknown" {
		t.Fatalf("missing PVC accepted %+v %v", r, e)
	}
	gets := 0
	c.PrependReactor("get", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
		gets++
		n := replacementNode()
		if gets == 2 {
			n.UID = "replacement-uid"
		}
		return true, n, nil
	})
	if _, e = m.ReplacementImpact(context.Background(), "worker", "worker-uid"); e == nil {
		t.Fatal("concurrent Node identity change accepted")
	}
}

func TestReplacementImpactFindsUnmountedLocalPV(t *testing.T) {
	c := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "worker-uid", Labels: map[string]string{"kubernetes.io/hostname": "worker"}}}, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "orphaned-local", UID: "pv-uid"}, Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{Local: &corev1.LocalVolumeSource{Path: "/var/local/database"}}, PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: []string{"worker"}}}}}}}}})
	m := &K8sManager{clientset: c}
	r, e := m.ReplacementImpact(context.Background(), "worker", "worker-uid")
	if e != nil || !r.Unknown || !r.RequiresReview || len(r.Pods) != 0 || len(r.Volumes) != 1 || r.Volumes[0].PV != "orphaned-local" {
		t.Fatalf("unmounted local data lost from impact %+v %v", r, e)
	}
}

func TestWorkerReplacementIdentityBindsOwnedAddress(t *testing.T) {
	n := replacementNode()
	n.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.4"}}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	m := &K8sManager{clientset: fake.NewClientset(n)}
	uid, ready, e := m.WorkerReplacementIdentity(context.Background(), "worker", "10.0.0.4")
	if e != nil || !ready || uid != "worker-uid" {
		t.Fatalf("identity %s %v %v", uid, ready, e)
	}
	for _, address := range []string{"10.0.0.5", "worker", ""} {
		if _, _, e = m.WorkerReplacementIdentity(context.Background(), "worker", address); e == nil {
			t.Fatalf("accepted nonmatching address %q", address)
		}
	}
}
func TestVerifyStaleNodeAbsenceRequiresAuthoritativeNotFound(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientset()
	m := &K8sManager{clientset: c}
	if e := m.VerifyStaleNodeAbsent(ctx, "worker", "old-uid"); e != nil {
		t.Fatal(e)
	}
	c.CoreV1().Nodes().Create(ctx, replacementNode(), metav1.CreateOptions{})
	if e := m.VerifyStaleNodeAbsent(ctx, "worker", "old-uid"); e == nil {
		t.Fatal("different identity treated as absent")
	}
	if e := m.VerifyStaleNodeAbsent(ctx, "worker", "worker-uid"); e == nil {
		t.Fatal("existing stale node treated as absent")
	}
	c.PrependReactor("get", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("provider API unavailable")
	})
	if e := m.VerifyStaleNodeAbsent(ctx, "worker", "worker-uid"); e == nil {
		t.Fatal("unknown API state treated as absent")
	}
}

func TestReplacementDrainRejectsMissingGuardAndChangedNode(t *testing.T) {
	for _, changed := range []bool{false, true} {
		c := fake.NewClientset(replacementNode())
		m := &K8sManager{clientset: c}
		patches := 0
		c.PrependReactor("patch", "nodes", func(a ktesting.Action) (bool, runtime.Object, error) {
			patches++
			var ops []map[string]any
			if json.Unmarshal(a.(ktesting.PatchAction).GetPatch(), &ops) != nil || len(ops) != 3 || ops[0]["value"] != "worker-uid" || ops[1]["value"] != "42" {
				t.Fatal("cordon lacks UID/resourceVersion tests")
			}
			return true, replacementNode(), nil
		})
		c.PrependReactor("get", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
			n := replacementNode()
			if patches > 0 {
				n.UID = "new-node"
			}
			return true, n, nil
		})
		ctx := context.Background()
		if changed {
			ctx = reconcile.WithMutationGuard(ctx, func(context.Context) error { return nil })
		}
		if err := m.CordonAndDrainReplacement(ctx, "worker", "worker-uid", true); err == nil {
			t.Fatal("unsafe drain accepted")
		}
		if !changed && patches != 0 {
			t.Fatal("missing authority admitted cordon")
		}
		for _, a := range c.Actions() {
			if a.GetSubresource() == "eviction" {
				t.Fatal("evicted after node identity change")
			}
		}
	}
}

func TestReplacementDrainRechecksAuthorityAndPinsPod(t *testing.T) {
	for _, lost := range []bool{false, true} {
		controller := true
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "work", Namespace: "ns", UID: "pod-original", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rs", Controller: &controller}}}, Spec: corev1.PodSpec{NodeName: "worker"}}
		c := fake.NewClientset(replacementNode(), pod)
		m := &K8sManager{clientset: c}
		checks := 0
		evicted := 0
		c.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
			if a.GetSubresource() != "eviction" {
				return false, nil, nil
			}
			evicted++
			e := a.(ktesting.CreateAction).GetObject().(*policyv1.Eviction)
			if e.DeleteOptions == nil || e.DeleteOptions.Preconditions == nil || e.DeleteOptions.Preconditions.UID == nil || *e.DeleteOptions.Preconditions.UID != "pod-original" {
				t.Fatal("eviction not pinned")
			}
			return true, nil, c.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), "ns", "work")
		})
		ctx := reconcile.WithMutationGuard(context.Background(), func(context.Context) error {
			checks++
			if lost && checks > 1 {
				return reconcile.ErrAuthority
			}
			return nil
		})
		err := m.CordonAndDrainReplacement(ctx, "worker", "worker-uid", true)
		if lost {
			if err == nil || evicted != 0 {
				t.Fatal("lost lease still evicted")
			}
		} else if err != nil || evicted != 1 {
			t.Fatalf("drain: %v evicted %d", err, evicted)
		}
	}
}

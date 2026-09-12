package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"talosdeck/internal/reconcile"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
)

type ReplacementImpactReport struct {
	NodeName       string                    `json:"nodeName"`
	NodeUID        string                    `json:"nodeUID"`
	ProviderID     string                    `json:"providerID,omitempty"`
	ObservedAt     time.Time                 `json:"observedAt"`
	Unknown        bool                      `json:"unknown"`
	RequiresReview bool                      `json:"requiresReview"`
	Issues         []string                  `json:"issues"`
	Pods           []ReplacementPodImpact    `json:"pods"`
	Volumes        []ReplacementVolumeImpact `json:"volumes"`
}
type ReplacementPodImpact struct {
	Namespace      string          `json:"namespace"`
	Name           string          `json:"name"`
	UID            string          `json:"uid"`
	Phase          corev1.PodPhase `json:"phase"`
	ControllerKind string          `json:"controllerKind,omitempty"`
	ControllerUID  string          `json:"controllerUID,omitempty"`
	ControllerName string          `json:"controllerName,omitempty"`
	RequiresReview bool            `json:"requiresReview"`
	Reasons        []string        `json:"reasons"`
}
type ReplacementAttachment struct {
	Name           string `json:"name"`
	UID            string `json:"uid"`
	NodeName       string `json:"nodeName"`
	Attacher       string `json:"attacher"`
	Attached       bool   `json:"attached"`
	Deleting       bool   `json:"deleting"`
	HasAttachError bool   `json:"hasAttachError"`
	HasDetachError bool   `json:"hasDetachError"`
}
type ReplacementVolumeImpact struct {
	Namespace         string                               `json:"namespace"`
	Pod               string                               `json:"pod"`
	Volume            string                               `json:"volume"`
	Kind              string                               `json:"kind"`
	State             string                               `json:"state"`
	Reasons           []string                             `json:"reasons"`
	PVC               string                               `json:"pvc,omitempty"`
	PVCUID            string                               `json:"pvcUID,omitempty"`
	PV                string                               `json:"pv,omitempty"`
	PVUID             string                               `json:"pvUID,omitempty"`
	StorageClass      string                               `json:"storageClass,omitempty"`
	Provisioner       string                               `json:"provisioner,omitempty"`
	ReclaimPolicy     corev1.PersistentVolumeReclaimPolicy `json:"reclaimPolicy,omitempty"`
	CSIDriver         string                               `json:"csiDriver,omitempty"`
	CSIAttachRequired *bool                                `json:"csiAttachRequired,omitempty"`
	AccessModes       []corev1.PersistentVolumeAccessMode  `json:"accessModes,omitempty"`
	NodeAffinity      *corev1.VolumeNodeAffinity           `json:"nodeAffinity,omitempty"`
	VolumeBindingMode *storagev1.VolumeBindingMode         `json:"volumeBindingMode,omitempty"`
	AllowedTopologies []corev1.TopologySelectorTerm        `json:"allowedTopologies,omitempty"`
	LocalPath         string                               `json:"localPath,omitempty"`
	Attachments       []ReplacementAttachment              `json:"attachments"`
}

func workerIdentity(node *corev1.Node, name, expectedUID string) error {
	if expectedUID == "" || name == "" || node == nil || node.Name != name || string(node.UID) != expectedUID {
		return errors.New("worker identity does not match pinned Kubernetes Node UID")
	}
	for _, key := range []string{"node-role.kubernetes.io/control-plane", "node-role.kubernetes.io/controlplane", "node-role.kubernetes.io/master"} {
		if _, ok := node.Labels[key]; ok {
			return errors.New("control-plane node is forbidden in worker replacement")
		}
	}
	for _, taint := range node.Spec.Taints {
		if strings.Contains(taint.Key, "node-role.kubernetes.io/") && (strings.HasSuffix(taint.Key, "control-plane") || strings.HasSuffix(taint.Key, "controlplane") || strings.HasSuffix(taint.Key, "master")) {
			return errors.New("control-plane taint is forbidden in worker replacement")
		}
	}
	for _, key := range []string{"kubernetes.io/role", "node-role"} {
		if role := strings.ToLower(node.Labels[key]); role != "" && role != "worker" {
			return errors.New("node role does not confirm a worker")
		}
	}
	return nil
}

// DeleteStaleNode is only the identity-conditional Kubernetes deletion primitive.
// The caller must establish fresh durable provider fencing before invoking it.
// A missing node is not silently treated as success; reconciliation must prove it.
func (m *K8sManager) DeleteStaleNode(ctx context.Context, name, expectedUID string) error {
	if m == nil || m.clientset == nil {
		return errors.New("Kubernetes client unavailable")
	}
	node, e := m.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if e != nil {
		return e
	}
	if e = workerIdentity(node, name, expectedUID); e != nil {
		return e
	}
	uid := types.UID(expectedUID)
	rv := node.ResourceVersion
	if e := reconcile.CheckRequiredMutation(ctx); e != nil {
		return e
	}
	return m.clientset.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
}

// CordonAndDrainReplacement pins both the Node and each evicted Pod identity.
// It cannot cordon a replacement Node that happens to reuse the old name.
func (m *K8sManager) CordonAndDrainReplacement(ctx context.Context, name, expectedUID string, ackEmptyDir bool) error {
	if m == nil || m.clientset == nil {
		return errors.New("Kubernetes client unavailable")
	}
	node, err := m.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = workerIdentity(node, name, expectedUID); err != nil {
		return err
	}
	if node.ResourceVersion == "" {
		return errors.New("node resource version unavailable")
	}
	patch, err := json.Marshal([]map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": expectedUID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": node.ResourceVersion},
		{"op": "add", "path": "/spec/unschedulable", "value": true},
	})
	if err != nil {
		return err
	}
	if err = reconcile.CheckRequiredMutation(ctx); err != nil {
		return err
	}
	if _, err = m.clientset.CoreV1().Nodes().Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{}); err != nil {
		return err
	}
	return m.drainNode(ctx, name, expectedUID, ackEmptyDir)
}

// ReplacementImpact observes storage references; it never claims drain migrates
// data. Persistent volumes require review even when topology appears portable:
// driver detach/attach and application recovery cannot be proved from a PVC.
func (m *K8sManager) ReplacementImpact(ctx context.Context, name, expectedUID string) (ReplacementImpactReport, error) {
	r := ReplacementImpactReport{NodeName: name, NodeUID: expectedUID, ObservedAt: time.Now().UTC(), Issues: []string{}, Pods: []ReplacementPodImpact{}, Volumes: []ReplacementVolumeImpact{}}
	if m == nil || m.clientset == nil {
		return r, errors.New("Kubernetes client unavailable")
	}
	node, e := m.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if e != nil {
		return r, e
	}
	if e = workerIdentity(node, name, expectedUID); e != nil {
		return r, e
	}
	r.ProviderID = node.Spec.ProviderID
	unknown := func(reason string) { r.Unknown = true; r.RequiresReview = true; r.Issues = append(r.Issues, reason) }
	attachments := map[string][]ReplacementAttachment{}
	attachmentCount := 0
	token := ""
	for {
		list, e := m.clientset.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{Limit: 500, Continue: token})
		if e != nil {
			unknown("volumeattachment-inventory-unavailable")
			break
		}
		attachmentCount += len(list.Items)
		if attachmentCount > 10000 {
			unknown("volumeattachment-inventory-limit")
			break
		}
		for _, a := range list.Items {
			if a.Spec.Source.PersistentVolumeName == nil {
				if a.Spec.NodeName == name {
					unknown("inline-volumeattachment-requires-review")
				}
				continue
			}
			pv := *a.Spec.Source.PersistentVolumeName
			attachments[pv] = append(attachments[pv], ReplacementAttachment{Name: a.Name, UID: string(a.UID), NodeName: a.Spec.NodeName, Attacher: a.Spec.Attacher, Attached: a.Status.Attached, Deleting: a.DeletionTimestamp != nil, HasAttachError: a.Status.AttachError != nil, HasDetachError: a.Status.DetachError != nil})
		}
		if list.Continue == "" {
			break
		}
		if list.Continue == token {
			unknown("volumeattachment-pagination-stalled")
			break
		}
		token = list.Continue
	}
	token = ""
	for {
		list, e := m.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name, Limit: 500, Continue: token})
		if e != nil {
			unknown("pod-inventory-unavailable")
			break
		}
		if len(r.Pods)+len(list.Items) > 10000 {
			unknown("pod-inventory-limit")
			break
		}
		for _, pod := range list.Items {
			if pod.Spec.NodeName != name {
				continue
			}
			p := ReplacementPodImpact{Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID), Phase: pod.Status.Phase, Reasons: []string{}}
			if c := metav1.GetControllerOf(&pod); c != nil {
				p.ControllerKind = c.Kind
				p.ControllerName = c.Name
				p.ControllerUID = string(c.UID)
			} else if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
				p.RequiresReview = true
				p.Reasons = append(p.Reasons, "unmanaged-pod-not-automatically-recreated")
			}
			if _, mirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
				p.RequiresReview = true
				p.Reasons = append(p.Reasons, "static-pod-local-configuration")
			}
			for _, volume := range pod.Spec.Volumes {
				if len(r.Volumes) >= 10000 {
					unknown("volume-inventory-limit")
					break
				}
				v := ReplacementVolumeImpact{Namespace: pod.Namespace, Pod: pod.Name, Volume: volume.Name, State: "portable", Reasons: []string{}, Attachments: []ReplacementAttachment{}}
				switch {
				case volume.PersistentVolumeClaim != nil:
					v.Kind = "pvc"
					v.PVC = volume.PersistentVolumeClaim.ClaimName
					m.replacementPVC(ctx, &v, attachments)
				case volume.Ephemeral != nil:
					v.Kind = "ephemeral-pvc"
					v.PVC = pod.Name + "-" + volume.Name
					m.replacementPVC(ctx, &v, attachments)
					v.Reasons = append(v.Reasons, "ephemeral-claim-lifetime-bound-to-pod")
					if v.State != "unknown" {
						v.State = "requires_review"
					}
				case volume.HostPath != nil:
					v.Kind = "hostPath"
					v.LocalPath = volume.HostPath.Path
					v.State = "requires_review"
					v.Reasons = append(v.Reasons, "host-local-data-not-migrated")
				case volume.EmptyDir != nil:
					v.Kind = "emptyDir"
					v.State = "requires_review"
					v.Reasons = append(v.Reasons, "ephemeral-data-will-be-lost")
				case volume.Secret != nil || volume.ConfigMap != nil || volume.Projected != nil || volume.DownwardAPI != nil:
					v.Kind = "api-projected"
					v.Reasons = append(v.Reasons, "no-persistent-volume-data")
				case volume.CSI != nil:
					v.Kind = "inline-csi"
					v.CSIDriver = volume.CSI.Driver
					v.State = "unknown"
					v.Reasons = append(v.Reasons, "inline-csi-lifecycle-and-recovery-unverified")
				default:
					v.Kind = "unsupported-volume-source"
					v.State = "unknown"
					v.Reasons = append(v.Reasons, "volume-migration-capability-unknown")
				}
				if v.State != "portable" {
					p.RequiresReview = true
					r.RequiresReview = true
				}
				if v.State == "unknown" {
					r.Unknown = true
				}
				r.Volumes = append(r.Volumes, v)
			}
			r.RequiresReview = r.RequiresReview || p.RequiresReview
			r.Pods = append(r.Pods, p)
		}
		if list.Continue == "" {
			break
		}
		if list.Continue == token {
			unknown("pod-pagination-stalled")
			break
		}
		token = list.Continue
	}
	// Pods can already be gone after a worker failure. Include node-bound PVs
	// and attachments even when no surviving Pod currently references their PVC.
	seenPV := map[string]bool{}
	for _, v := range r.Volumes {
		seenPV[v.PV] = true
	}
	token = ""
	pvCount := 0
	for {
		inventory, e := m.clientset.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{Limit: 500, Continue: token})
		if e != nil {
			unknown("unmounted-pv-inventory-unavailable")
			break
		}
		pvCount += len(inventory.Items)
		if pvCount > 10000 {
			unknown("unmounted-pv-inventory-limit")
			break
		}
		for _, pv := range inventory.Items {
			if seenPV[pv.Name] {
				continue
			}
			attachedHere := false
			for _, a := range attachments[pv.Name] {
				if a.NodeName == name {
					attachedHere = true
				}
			}
			matched, known := replacementAffinity(pv.Spec.NodeAffinity, node)
			local := pv.Spec.Local != nil || pv.Spec.HostPath != nil
			if !attachedHere && !(local && (matched || !known)) {
				continue
			}
			if len(r.Volumes) >= 10000 {
				unknown("volume-inventory-limit")
				break
			}
			v := ReplacementVolumeImpact{Volume: "unmounted:" + pv.Name, PV: pv.Name, PVUID: string(pv.UID), Kind: "unmounted-pv", State: "requires_review", Reasons: []string{"node-bound-volume-without-surviving-pod"}, Attachments: []ReplacementAttachment{}}
			if pv.Spec.ClaimRef != nil && pv.Spec.ClaimRef.Name != "" && pv.Spec.ClaimRef.Namespace != "" {
				v.Namespace = pv.Spec.ClaimRef.Namespace
				v.PVC = pv.Spec.ClaimRef.Name
				m.replacementPVC(ctx, &v, attachments)
				if v.PV != pv.Name || v.PVUID != string(pv.UID) {
					v.State = "unknown"
					v.Reasons = append(v.Reasons, "unmounted-pv-claim-identity-changed")
				}
			} else {
				v.State = "unknown"
				v.Reasons = append(v.Reasons, "unmounted-pv-claim-unavailable")
				v.Attachments = append(v.Attachments, attachments[pv.Name]...)
				v.NodeAffinity = pv.Spec.NodeAffinity.DeepCopy()
				v.ReclaimPolicy = pv.Spec.PersistentVolumeReclaimPolicy
				if pv.Spec.Local != nil {
					v.LocalPath = pv.Spec.Local.Path
					v.Kind = "local-pv"
				}
				if pv.Spec.HostPath != nil {
					v.LocalPath = pv.Spec.HostPath.Path
					v.Kind = "hostPath-pv"
				}
			}
			if !known && !attachedHere {
				v.State = "unknown"
				v.Reasons = append(v.Reasons, "local-volume-node-affinity-unknown")
			}
			r.RequiresReview = true
			if v.State == "unknown" {
				r.Unknown = true
			}
			r.Volumes = append(r.Volumes, v)
		}
		if inventory.Continue == "" {
			break
		}
		if inventory.Continue == token {
			unknown("pv-pagination-stalled")
			break
		}
		token = inventory.Continue
	}
	// A name may have been rebound during the inventory operation.
	current, e := m.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if e != nil {
		return r, e
	}
	if e = workerIdentity(current, name, expectedUID); e != nil {
		return r, e
	}
	sort.Slice(r.Pods, func(i, j int) bool {
		return r.Pods[i].Namespace+"/"+r.Pods[i].Name < r.Pods[j].Namespace+"/"+r.Pods[j].Name
	})
	sort.Slice(r.Volumes, func(i, j int) bool {
		a, b := r.Volumes[i], r.Volumes[j]
		return a.Namespace+"/"+a.Pod+"/"+a.Volume < b.Namespace+"/"+b.Pod+"/"+b.Volume
	})
	return r, nil
}
func (m *K8sManager) replacementPVC(ctx context.Context, v *ReplacementVolumeImpact, attachments map[string][]ReplacementAttachment) {
	fail := func(reason string) { v.State = "unknown"; v.Reasons = append(v.Reasons, reason) }
	v.State = "requires_review"
	v.Reasons = append(v.Reasons, "persistent-data-detach-attach-and-recovery-require-review")
	pvc, e := m.clientset.CoreV1().PersistentVolumeClaims(v.Namespace).Get(ctx, v.PVC, metav1.GetOptions{})
	if e != nil {
		fail("pvc-unavailable")
		return
	}
	v.PVCUID = string(pvc.UID)
	if pvc.UID == "" {
		fail("pvc-identity-missing")
	}
	v.PV = pvc.Spec.VolumeName
	v.AccessModes = append([]corev1.PersistentVolumeAccessMode{}, pvc.Spec.AccessModes...)
	if pvc.Spec.StorageClassName != nil {
		v.StorageClass = *pvc.Spec.StorageClassName
	}
	if pvc.DeletionTimestamp != nil {
		fail("pvc-deleting")
	}
	if pvc.Status.Phase != corev1.ClaimBound || v.PV == "" {
		fail("pvc-not-bound")
		return
	}
	pv, e := m.clientset.CoreV1().PersistentVolumes().Get(ctx, v.PV, metav1.GetOptions{})
	if e != nil {
		fail("pv-unavailable")
		return
	}
	v.PVUID = string(pv.UID)
	if pv.UID == "" {
		fail("pv-identity-missing")
	}
	v.ReclaimPolicy = pv.Spec.PersistentVolumeReclaimPolicy
	v.NodeAffinity = pv.Spec.NodeAffinity.DeepCopy()
	if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != pvc.UID || pv.Spec.ClaimRef.Name != pvc.Name || pv.Spec.ClaimRef.Namespace != pvc.Namespace {
		fail("pv-claim-identity-mismatch")
	}
	if pv.DeletionTimestamp != nil {
		fail("pv-deleting")
	}
	if pv.Spec.StorageClassName != v.StorageClass {
		fail("storage-class-identity-mismatch")
	}
	v.AccessModes = append([]corev1.PersistentVolumeAccessMode{}, pv.Spec.AccessModes...)
	v.Attachments = append(v.Attachments, attachments[v.PV]...)
	if v.ReclaimPolicy == corev1.PersistentVolumeReclaimDelete {
		v.Reasons = append(v.Reasons, "claim-deletion-may-delete-persistent-data")
	}
	if v.NodeAffinity != nil {
		v.Reasons = append(v.Reasons, "volume-node-affinity-constrains-replacement")
	}
	if pv.Spec.Local != nil {
		v.Kind = "local-pv"
		v.LocalPath = pv.Spec.Local.Path
		v.Reasons = append(v.Reasons, "local-volume-data-not-recovered-by-replacement")
	}
	if pv.Spec.HostPath != nil {
		v.Kind = "hostPath-pv"
		v.LocalPath = pv.Spec.HostPath.Path
		v.Reasons = append(v.Reasons, "host-local-data-not-migrated")
	}
	if v.StorageClass != "" {
		sc, e := m.clientset.StorageV1().StorageClasses().Get(ctx, v.StorageClass, metav1.GetOptions{})
		if e != nil {
			fail("storageclass-unavailable")
		} else {
			v.Provisioner = sc.Provisioner
			v.VolumeBindingMode = sc.VolumeBindingMode
			v.AllowedTopologies = sc.AllowedTopologies
		}
	}
	if pv.Spec.CSI != nil {
		v.CSIDriver = pv.Spec.CSI.Driver
		driver, e := m.clientset.StorageV1().CSIDrivers().Get(ctx, v.CSIDriver, metav1.GetOptions{})
		if e != nil {
			fail("csi-driver-capabilities-unavailable")
		} else {
			v.CSIAttachRequired = driver.Spec.AttachRequired
		}
		v.Reasons = append(v.Reasons, "csi-driver-detach-attach-must-be-verified")
	}
	for _, a := range v.Attachments {
		if v.CSIDriver != "" && a.Attacher != v.CSIDriver {
			fail("volumeattachment-driver-mismatch")
		}
		if a.Attached || a.Deleting || a.HasAttachError || a.HasDetachError {
			v.Reasons = append(v.Reasons, fmt.Sprintf("volumeattachment-%s-needs-detach-verification", a.Name))
		}
	}
}

func replacementAffinity(affinity *corev1.VolumeNodeAffinity, node *corev1.Node) (bool, bool) {
	if affinity == nil || affinity.Required == nil {
		return false, false
	}
	for _, term := range affinity.Required.NodeSelectorTerms {
		if len(term.MatchExpressions) == 0 && len(term.MatchFields) == 0 {
			continue
		}
		matches := true
		for _, group := range []struct {
			reqs   []corev1.NodeSelectorRequirement
			values labels.Set
		}{{term.MatchExpressions, labels.Set(node.Labels)}, {term.MatchFields, labels.Set{"metadata.name": node.Name}}} {
			for _, r := range group.reqs {
				var op selection.Operator
				switch r.Operator {
				case corev1.NodeSelectorOpIn:
					op = selection.In
				case corev1.NodeSelectorOpNotIn:
					op = selection.NotIn
				case corev1.NodeSelectorOpExists:
					op = selection.Exists
				case corev1.NodeSelectorOpDoesNotExist:
					op = selection.DoesNotExist
				case corev1.NodeSelectorOpGt:
					op = selection.GreaterThan
				case corev1.NodeSelectorOpLt:
					op = selection.LessThan
				default:
					return false, false
				}
				req, e := labels.NewRequirement(r.Key, op, r.Values)
				if e != nil {
					return false, false
				}
				if !req.Matches(group.values) {
					matches = false
				}
			}
		}
		if matches {
			return true, true
		}
	}
	return false, true
}

// WorkerReplacementIdentity resolves an operator-selected node to an immutable
// identity, requiring the provider-owned address to be present in Node status.
func (m *K8sManager) WorkerReplacementIdentity(ctx context.Context, name, expectedAddress string) (string, bool, error) {
	if m == nil || m.clientset == nil {
		return "", false, errors.New("Kubernetes client unavailable")
	}
	expectedIP := net.ParseIP(expectedAddress)
	if expectedIP == nil {
		return "", false, errors.New("provider-owned machine IP address is required")
	}
	node, e := m.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if e != nil {
		return "", false, e
	}
	if e = workerIdentity(node, name, string(node.UID)); e != nil {
		return "", false, e
	}
	matched := false
	for _, address := range node.Status.Addresses {
		if (address.Type == corev1.NodeInternalIP || address.Type == corev1.NodeExternalIP) && expectedIP.Equal(net.ParseIP(address.Address)) {
			matched = true
		}
	}
	if !matched {
		return "", false, errors.New("Kubernetes Node addresses do not match provider-owned machine identity")
	}
	ready := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			ready = condition.Status == corev1.ConditionTrue
		}
	}
	return string(node.UID), ready, nil
}

// VerifyStaleNodeAbsent requires an authoritative Kubernetes NotFound. A new
// object with the same name is not silently adopted or considered deleted.
func (m *K8sManager) VerifyStaleNodeAbsent(ctx context.Context, name, expectedUID string) error {
	if m == nil || m.clientset == nil {
		return errors.New("Kubernetes client unavailable")
	}
	if name == "" || expectedUID == "" {
		return errors.New("pinned node name and UID are required")
	}
	node, e := m.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if string(node.UID) != expectedUID {
		return errors.New("node name now belongs to another identity; operator review required")
	}
	return errors.New("stale Kubernetes Node still exists")
}

package k8s

import (
	"context"
	"encoding/json"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"strings"
	"testing"
)

func TestStorageLinksAndWorkloadSummariesExcludeSecretValues(t *testing.T) {
	ctx := context.Background()
	replicas := int32(2)
	sc := "local-path"
	client := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", Annotations: map[string]string{"credential": "annotation-private"}}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Env: []corev1.EnvVar{{Name: "PASSWORD", Value: "env-private"}}}}}}}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-123", Namespace: "apps"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "apps"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &sc, VolumeName: "pv-1"}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-1"}, Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{Name: "data", Namespace: "apps"}, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}, StorageClassName: sc}},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: sc}, Provisioner: "rancher.io/local-path", Parameters: map[string]string{"password": "storage-private"}},
	)
	manager := &K8sManager{clientset: client}
	workloads, err := manager.Workloads(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(workloads.Deployments) != 1 || workloads.Deployments[0].Ready != 1 || workloads.Deployments[0].Desired != 2 {
		t.Fatalf("bad deployment summary: %+v", workloads)
	}
	storage, err := manager.Storage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(storage.PersistentVolumeClaims) != 1 || len(storage.PersistentVolumeClaims[0].Pods) != 1 || storage.PersistentVolumes[0].Claim != "apps/data" {
		t.Fatalf("missing volume links: %+v", storage)
	}
	data, _ := json.Marshal([]any{workloads, storage})
	for _, secret := range []string{"annotation-private", "env-private", "storage-private"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("operational DTO exposes %s", secret)
		}
	}
}

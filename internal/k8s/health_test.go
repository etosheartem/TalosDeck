package k8s

import (
	"context"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"testing"
)

func TestHealthNetworkingIncludesFlannelNamespaceAndPropagates403(t *testing.T) {
	client := fake.NewClientset(&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "kube-flannel-ds", Namespace: "kube-flannel"}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "ghcr.io/flannel-io/flannel:v1"}}}}}, Status: appsv1.DaemonSetStatus{NumberReady: 3, DesiredNumberScheduled: 3}})
	m := &K8sManager{clientset: client}
	out, err := m.HealthNetworking(context.Background())
	if err != nil || len(out) != 1 || out[0].Namespace != "kube-flannel" || out[0].Desired != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	client.PrependReactor("list", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "daemonsets"}, "", nil)
	})
	if _, err := m.HealthNetworking(context.Background()); !apierrors.IsForbidden(err) {
		t.Fatal("403 became empty healthy inventory")
	}
}
func TestHealthClaimWaitForFirstConsumerHasExplicitScheduledConsumer(t *testing.T) {
	mode := storagev1.VolumeBindingWaitForFirstConsumer
	sc := "local"
	client := fake.NewClientset(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: sc}, VolumeBindingMode: &mode}, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: "ns"}, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &sc}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}}, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"}, Spec: corev1.PodSpec{NodeName: "worker", Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "claim"}}}}}})
	out, err := (&K8sManager{clientset: client}).HealthClaims(context.Background())
	if err != nil || len(out) != 1 || !out[0].WaitForConsumer || !out[0].HasConsumer {
		t.Fatalf("%+v %v", out, err)
	}
}

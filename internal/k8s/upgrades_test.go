package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func componentFixture() []runtime.Object {
	result := []runtime.Object{}
	for _, name := range []string{"cp-1", "cp-2", "cp-3"} {
		result = append(result, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}}})
		for _, component := range []string{"kube-apiserver", "kube-controller-manager", "kube-scheduler"} {
			result = append(result, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: component + "-" + name, Namespace: "kube-system", Labels: map[string]string{"component": component}}, Spec: corev1.PodSpec{NodeName: name, Containers: []corev1.Container{{Name: component, Image: "registry.k8s.io/" + component + ":v1.35.0"}}}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}})
		}
	}
	return result
}
func TestUpgradeVerifiesEveryControlPlaneComponent(t *testing.T) {
	for _, failure := range []string{"", "version", "not-ready", "missing", "image-digest"} {
		t.Run(failure, func(t *testing.T) {
			objects := componentFixture()
			pod := objects[len(objects)-1].(*corev1.Pod)
			switch failure {
			case "version":
				pod.Spec.Containers[0].Image = "registry.k8s.io/kube-scheduler:v1.34.0"
			case "not-ready":
				pod.Status.Conditions[0].Status = corev1.ConditionFalse
			case "missing":
				objects = objects[:len(objects)-1]
			case "image-digest":
				pod.Spec.Containers[0].Image = "registry.k8s.io/kube-scheduler@sha256:abc"
			}
			m := &K8sManager{clientset: fake.NewClientset(objects...)}
			err := m.VerifyUpgradeComponents(context.Background(), "1.35.0", true)
			if (err != nil) != (failure != "") {
				t.Fatalf("failure %s: %v", failure, err)
			}
		})
	}
}

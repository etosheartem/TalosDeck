package k8s

import (
	"context"
	"errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

type HealthWorkload struct {
	Name, Namespace, Kind string
	Images                []string
	Labels                map[string]string
	Ready, Desired        int32
}
type HealthClaim struct {
	Name, Namespace, Phase string
	CreatedAt              time.Time
	WaitForConsumer        bool
	HasConsumer            bool
}

func (m *K8sManager) HealthReady(ctx context.Context) error {
	if m == nil || m.clientset == nil {
		return errors.New("Kubernetes client unavailable")
	}
	client := m.clientset.Discovery().RESTClient()
	if client == nil {
		return errors.New("Kubernetes readiness client unavailable")
	}
	return client.Get().AbsPath("/readyz").Do(ctx).Error()
}
func (m *K8sManager) HealthNetworking(ctx context.Context) ([]HealthWorkload, error) {
	out := []HealthWorkload{}
	opts := metav1.ListOptions{Limit: 500}
	for {
		list, err := m.clientset.AppsV1().DaemonSets("").List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, d := range list.Items {
			w := HealthWorkload{Name: d.Name, Namespace: d.Namespace, Kind: "DaemonSet", Labels: d.Spec.Template.Labels, Ready: d.Status.NumberReady, Desired: d.Status.DesiredNumberScheduled}
			for _, c := range d.Spec.Template.Spec.Containers {
				w.Images = append(w.Images, c.Image)
			}
			out = append(out, w)
		}
		opts.Continue = list.Continue
		if opts.Continue == "" {
			break
		}
	}
	for {
		list, err := m.clientset.AppsV1().Deployments("").List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, d := range list.Items {
			desired := int32(1)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			w := HealthWorkload{Name: d.Name, Namespace: d.Namespace, Kind: "Deployment", Labels: d.Spec.Template.Labels, Ready: d.Status.ReadyReplicas, Desired: desired}
			for _, c := range d.Spec.Template.Spec.Containers {
				w.Images = append(w.Images, c.Image)
			}
			out = append(out, w)
		}
		opts.Continue = list.Continue
		if opts.Continue == "" {
			break
		}
	}
	return out, nil
}
func (m *K8sManager) HealthClaims(ctx context.Context) ([]HealthClaim, error) {
	classes, err := m.clientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	wait := map[string]bool{}
	for _, c := range classes.Items {
		wait[c.Name] = c.VolumeBindingMode != nil && string(*c.VolumeBindingMode) == "WaitForFirstConsumer"
	}
	consumers := map[string]bool{}
	opts := metav1.ListOptions{Limit: 500}
	for {
		pods, err := m.clientset.CoreV1().Pods("").List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, p := range pods.Items {
			if p.Spec.NodeName == "" {
				continue
			}
			for _, v := range p.Spec.Volumes {
				if v.PersistentVolumeClaim != nil {
					consumers[p.Namespace+"/"+v.PersistentVolumeClaim.ClaimName] = true
				}
			}
		}
		opts.Continue = pods.Continue
		if opts.Continue == "" {
			break
		}
	}
	out := []HealthClaim{}
	for {
		claims, err := m.clientset.CoreV1().PersistentVolumeClaims("").List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, c := range claims.Items {
			sc := ""
			if c.Spec.StorageClassName != nil {
				sc = *c.Spec.StorageClassName
			}
			out = append(out, HealthClaim{Name: c.Name, Namespace: c.Namespace, Phase: string(c.Status.Phase), CreatedAt: c.CreationTimestamp.Time, WaitForConsumer: wait[sc], HasConsumer: consumers[c.Namespace+"/"+c.Name]})
		}
		opts.Continue = claims.Continue
		if opts.Continue == "" {
			break
		}
	}
	return out, nil
}

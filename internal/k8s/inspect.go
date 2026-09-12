package k8s

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Explicit DTOs keep environment values, annotations, commands and Secret
// references out of operational exports. Raw Kubernetes objects are not sent.
type WorkloadInfo struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Kind      string            `json:"kind"`
	Ready     int32             `json:"ready"`
	Desired   int32             `json:"desired"`
	Status    string            `json:"status"`
	CreatedAt time.Time         `json:"createdAt"`
	Selector  map[string]string `json:"selector,omitempty"`
}
type WorkloadInventory struct {
	Deployments  []WorkloadInfo `json:"deployments"`
	DaemonSets   []WorkloadInfo `json:"daemonsets"`
	StatefulSets []WorkloadInfo `json:"statefulsets"`
	Jobs         []WorkloadInfo `json:"jobs"`
	CronJobs     []WorkloadInfo `json:"cronjobs"`
}

func (m *K8sManager) Workloads(ctx context.Context, namespace string) (*WorkloadInventory, error) {
	out := &WorkloadInventory{Deployments: []WorkloadInfo{}, DaemonSets: []WorkloadInfo{}, StatefulSets: []WorkloadInfo{}, Jobs: []WorkloadInfo{}, CronJobs: []WorkloadInfo{}}
	opts := metav1.ListOptions{Limit: 500}
	for {
		xs, err := m.clientset.AppsV1().Deployments(namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, x := range xs.Items {
			desired := int32(1)
			if x.Spec.Replicas != nil {
				desired = *x.Spec.Replicas
			}
			out.Deployments = append(out.Deployments, WorkloadInfo{Name: x.Name, Namespace: x.Namespace, Kind: "Deployment", Ready: x.Status.ReadyReplicas, Desired: desired, CreatedAt: x.CreationTimestamp.Time, Selector: x.Spec.Selector.MatchLabels})
		}
		opts.Continue = xs.Continue
		if opts.Continue == "" {
			break
		}
	}
	for {
		xs, err := m.clientset.AppsV1().DaemonSets(namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, x := range xs.Items {
			out.DaemonSets = append(out.DaemonSets, WorkloadInfo{Name: x.Name, Namespace: x.Namespace, Kind: "DaemonSet", Ready: x.Status.NumberReady, Desired: x.Status.DesiredNumberScheduled, CreatedAt: x.CreationTimestamp.Time, Selector: x.Spec.Selector.MatchLabels})
		}
		opts.Continue = xs.Continue
		if opts.Continue == "" {
			break
		}
	}
	for {
		xs, err := m.clientset.AppsV1().StatefulSets(namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, x := range xs.Items {
			desired := int32(1)
			if x.Spec.Replicas != nil {
				desired = *x.Spec.Replicas
			}
			out.StatefulSets = append(out.StatefulSets, WorkloadInfo{Name: x.Name, Namespace: x.Namespace, Kind: "StatefulSet", Ready: x.Status.ReadyReplicas, Desired: desired, CreatedAt: x.CreationTimestamp.Time, Selector: x.Spec.Selector.MatchLabels})
		}
		opts.Continue = xs.Continue
		if opts.Continue == "" {
			break
		}
	}
	for {
		xs, err := m.clientset.BatchV1().Jobs(namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, x := range xs.Items {
			desired := int32(1)
			if x.Spec.Completions != nil {
				desired = *x.Spec.Completions
			}
			status := "Running"
			if x.Status.Succeeded >= desired {
				status = "Complete"
			}
			if x.Status.Failed > 0 {
				status = "Failed"
			}
			out.Jobs = append(out.Jobs, WorkloadInfo{Name: x.Name, Namespace: x.Namespace, Kind: "Job", Ready: x.Status.Succeeded, Desired: desired, Status: status, CreatedAt: x.CreationTimestamp.Time})
		}
		opts.Continue = xs.Continue
		if opts.Continue == "" {
			break
		}
	}
	for {
		xs, err := m.clientset.BatchV1().CronJobs(namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, x := range xs.Items {
			status := x.Spec.Schedule
			if x.Spec.Suspend != nil && *x.Spec.Suspend {
				status = "Suspended"
			}
			out.CronJobs = append(out.CronJobs, WorkloadInfo{Name: x.Name, Namespace: x.Namespace, Kind: "CronJob", Ready: int32(len(x.Status.Active)), Status: status, CreatedAt: x.CreationTimestamp.Time})
		}
		opts.Continue = xs.Continue
		if opts.Continue == "" {
			break
		}
	}
	return out, nil
}

type EventInfo struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Type      string    `json:"type"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Count     int32     `json:"count"`
	Time      time.Time `json:"time"`
}

func (m *K8sManager) Events(ctx context.Context, namespace, name string) ([]EventInfo, error) {
	opts := metav1.ListOptions{Limit: 500}
	if name != "" {
		opts.FieldSelector = "involvedObject.name=" + name
	}
	out := []EventInfo{}
	for {
		xs, err := m.clientset.CoreV1().Events(namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, x := range xs.Items {
			stamp := x.LastTimestamp.Time
			if stamp.IsZero() {
				stamp = x.EventTime.Time
			}
			out = append(out, EventInfo{Namespace: x.Namespace, Name: x.InvolvedObject.Name, Kind: x.InvolvedObject.Kind, Type: x.Type, Reason: x.Reason, Message: x.Message, Count: x.Count, Time: stamp})
		}
		opts.Continue = xs.Continue
		if opts.Continue == "" {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > 1000 {
		out = out[:1000]
	}
	return out, nil
}

type VolumeInfo struct {
	Name          string   `json:"name"`
	Namespace     string   `json:"namespace,omitempty"`
	Status        string   `json:"status,omitempty"`
	Capacity      string   `json:"capacity,omitempty"`
	StorageClass  string   `json:"storageClass,omitempty"`
	Volume        string   `json:"volume,omitempty"`
	Claim         string   `json:"claim,omitempty"`
	Pods          []string `json:"pods"`
	AccessModes   []string `json:"accessModes,omitempty"`
	ReclaimPolicy string   `json:"reclaimPolicy,omitempty"`
}
type StorageClassInfo struct {
	Name          string `json:"name"`
	Provisioner   string `json:"provisioner"`
	ReclaimPolicy string `json:"reclaimPolicy"`
	BindingMode   string `json:"bindingMode"`
	Default       bool   `json:"default"`
}
type StorageInventory struct {
	PersistentVolumes      []VolumeInfo       `json:"persistentVolumes"`
	PersistentVolumeClaims []VolumeInfo       `json:"persistentVolumeClaims"`
	StorageClasses         []StorageClassInfo `json:"storageClasses"`
}

func (m *K8sManager) Storage(ctx context.Context) (*StorageInventory, error) {
	out := &StorageInventory{PersistentVolumes: []VolumeInfo{}, PersistentVolumeClaims: []VolumeInfo{}, StorageClasses: []StorageClassInfo{}}
	opts := metav1.ListOptions{Limit: 500}
	claims := map[string][]string{}
	for {
		ps, err := m.clientset.CoreV1().Pods("").List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, p := range ps.Items {
			for _, v := range p.Spec.Volumes {
				if v.PersistentVolumeClaim != nil {
					k := p.Namespace + "/" + v.PersistentVolumeClaim.ClaimName
					claims[k] = append(claims[k], p.Name)
				}
			}
		}
		opts.Continue = ps.Continue
		if opts.Continue == "" {
			break
		}
	}
	for {
		xs, err := m.clientset.CoreV1().PersistentVolumeClaims("").List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, x := range xs.Items {
			sc := ""
			if x.Spec.StorageClassName != nil {
				sc = *x.Spec.StorageClassName
			}
			modes := []string{}
			for _, a := range x.Spec.AccessModes {
				modes = append(modes, string(a))
			}
			capacity := x.Status.Capacity[corev1.ResourceStorage]
			pods := claims[x.Namespace+"/"+x.Name]
			if pods == nil {
				pods = []string{}
			}
			out.PersistentVolumeClaims = append(out.PersistentVolumeClaims, VolumeInfo{Name: x.Name, Namespace: x.Namespace, Status: string(x.Status.Phase), Capacity: capacity.String(), StorageClass: sc, Volume: x.Spec.VolumeName, Pods: pods, AccessModes: modes})
		}
		opts.Continue = xs.Continue
		if opts.Continue == "" {
			break
		}
	}
	for {
		xs, err := m.clientset.CoreV1().PersistentVolumes().List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, x := range xs.Items {
			claim := ""
			if x.Spec.ClaimRef != nil {
				claim = x.Spec.ClaimRef.Namespace + "/" + x.Spec.ClaimRef.Name
			}
			capacity := x.Spec.Capacity[corev1.ResourceStorage]
			out.PersistentVolumes = append(out.PersistentVolumes, VolumeInfo{Name: x.Name, Status: string(x.Status.Phase), Capacity: capacity.String(), StorageClass: x.Spec.StorageClassName, Claim: claim, Pods: claims[claim], ReclaimPolicy: string(x.Spec.PersistentVolumeReclaimPolicy)})
		}
		opts.Continue = xs.Continue
		if opts.Continue == "" {
			break
		}
	}
	for {
		xs, err := m.clientset.StorageV1().StorageClasses().List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, x := range xs.Items {
			r, b := "", ""
			if x.ReclaimPolicy != nil {
				r = string(*x.ReclaimPolicy)
			}
			if x.VolumeBindingMode != nil {
				b = string(*x.VolumeBindingMode)
			}
			out.StorageClasses = append(out.StorageClasses, StorageClassInfo{Name: x.Name, Provisioner: x.Provisioner, ReclaimPolicy: r, BindingMode: b, Default: x.Annotations["storageclass.kubernetes.io/is-default-class"] == "true"})
		}
		opts.Continue = xs.Continue
		if opts.Continue == "" {
			break
		}
	}
	return out, nil
}

type ContainerInfo struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts"`
	State    string `json:"state"`
}
type PodInspector struct {
	Pod        PodInfo         `json:"pod"`
	Containers []ContainerInfo `json:"containers"`
	Events     []EventInfo     `json:"events"`
	Logs       string          `json:"logs"`
	Volumes    []VolumeInfo    `json:"volumes"`
	Warnings   []string        `json:"warnings"`
}

func (m *K8sManager) InspectPod(ctx context.Context, namespace, name, container string) (*PodInspector, error) {
	p, err := m.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	out := &PodInspector{Pod: PodInfo{ID: string(p.UID), Name: p.Name, Namespace: p.Namespace, Node: p.Spec.NodeName, NodeName: p.Spec.NodeName, Phase: string(p.Status.Phase), Status: string(p.Status.Phase), CreatedAt: p.CreationTimestamp.Time, PodIP: p.Status.PodIP}, Containers: []ContainerInfo{}, Events: []EventInfo{}, Volumes: []VolumeInfo{}, Warnings: []string{}}
	for _, c := range p.Status.ContainerStatuses {
		state := containerStateStatus(c.State)
		if state == "" && c.State.Running != nil {
			state = "Running"
		}
		out.Containers = append(out.Containers, ContainerInfo{Name: c.Name, Image: c.Image, Ready: c.Ready, Restarts: c.RestartCount, State: state})
		out.Pod.Restarts += c.RestartCount
		if c.Ready {
			out.Pod.ReadyCount++
		}
	}
	out.Pod.TotalContainers = len(p.Spec.Containers)
	out.Pod.ContainerCount = fmt.Sprintf("%d/%d", out.Pod.ReadyCount, out.Pod.TotalContainers)
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			out.Volumes = append(out.Volumes, VolumeInfo{Name: v.Name, Namespace: namespace, Claim: v.PersistentVolumeClaim.ClaimName, Pods: []string{name}})
		}
	}
	out.Events, err = m.Events(ctx, namespace, name)
	if err != nil {
		out.Events = []EventInfo{}
		out.Warnings = append(out.Warnings, "Events unavailable")
	}
	if container == "" && len(p.Spec.Containers) > 0 {
		container = p.Spec.Containers[0].Name
	}
	valid := false
	for _, c := range p.Spec.Containers {
		if c.Name == container {
			valid = true
		}
	}
	if !valid {
		return nil, fmt.Errorf("unknown container")
	}
	tail, limit := int64(200), int64(256*1024)
	stream, err := m.clientset.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{Container: container, TailLines: &tail, LimitBytes: &limit, Timestamps: true}).Stream(ctx)
	if err != nil {
		out.Warnings = append(out.Warnings, "Container logs unavailable")
	} else {
		defer stream.Close()
		data, e := io.ReadAll(io.LimitReader(stream, limit))
		if e == nil {
			out.Logs = string(data)
		} else {
			out.Warnings = append(out.Warnings, "Container log read failed")
		}
	}
	return out, nil
}

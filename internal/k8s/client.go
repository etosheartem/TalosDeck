package k8s

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// K8sManager manages interaction with the Kubernetes cluster workloads.
type K8sManager struct {
	clientset kubernetes.Interface
}

// NewK8sManager initializes Kubernetes client using kubeconfig file or raw talos kubeconfig fallback.
func NewK8sManager(kubeconfigPath string, kubeconfigBytesProvider func(ctx context.Context) ([]byte, error)) (*K8sManager, error) {
	if kubeconfigPath == "" {
		kubeconfigPath = os.Getenv("KUBECONFIG")
	}
	if kubeconfigPath == "" {
		defaultPath := "/home/artem/laba-kuber/kubeconfig"
		if _, err := os.Stat(defaultPath); err == nil {
			kubeconfigPath = defaultPath
		}
	}

	var restConfig *rest.Config
	var err error

	if kubeconfigPath != "" {
		if _, statErr := os.Stat(kubeconfigPath); statErr == nil {
			restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		}
	}

	if restConfig == nil && kubeconfigBytesProvider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rawBytes, kErr := kubeconfigBytesProvider(ctx)
		if kErr == nil && len(rawBytes) > 0 {
			restConfig, err = clientcmd.RESTConfigFromKubeConfig(rawBytes)
		}
	}

	if restConfig == nil {
		if err != nil {
			return nil, fmt.Errorf("failed to build kubernetes config: %w", err)
		}
		return nil, fmt.Errorf("no valid kubeconfig found at %s", kubeconfigPath)
	}

	// Set reasonable timeouts
	restConfig.Timeout = 10 * time.Second

	cs, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	return &K8sManager{clientset: cs}, nil
}

// ListPods returns a summary list of pods matching the namespace and node filters.
func (m *K8sManager) ListPods(ctx context.Context, namespace, nodeFilter string) ([]PodInfo, error) {
	queryNS := namespace
	if queryNS == "all" {
		queryNS = ""
	}

	podList, err := m.clientset.CoreV1().Pods(queryNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}

	var results []PodInfo
	for _, p := range podList.Items {
		if nodeFilter != "" && p.Spec.NodeName != nodeFilter {
			continue
		}

		readyCount := 0
		restartCount := int32(0)
		status := string(p.Status.Phase)

		if p.DeletionTimestamp != nil {
			status = "Terminating"
		} else {
			// Check waiting reasons for better status (e.g. CrashLoopBackOff, ImagePullBackOff)
			for _, cs := range p.Status.ContainerStatuses {
				if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
					status = cs.State.Waiting.Reason
					break
				}
			}
		}

		for _, cs := range p.Status.ContainerStatuses {
			restartCount += cs.RestartCount
			if cs.Ready {
				readyCount++
			}
		}

		totalContainers := len(p.Spec.Containers)
		countStr := fmt.Sprintf("%d/%d", readyCount, totalContainers)

		ageStr := formatDuration(time.Since(p.CreationTimestamp.Time))

		results = append(results, PodInfo{
			Name:            p.Name,
			Namespace:       p.Namespace,
			Node:            p.Spec.NodeName,
			NodeIP:          p.Status.HostIP,
			Status:          status,
			Phase:           string(p.Status.Phase),
			ContainerCount:  countStr,
			ReadyContainers: readyCount,
			TotalContainers: totalContainers,
			Restarts:        restartCount,
			Age:             ageStr,
			CreatedAt:       p.CreationTimestamp.Time,
			PodIP:           p.Status.PodIP,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Namespace != results[j].Namespace {
			return results[i].Namespace < results[j].Namespace
		}
		return results[i].Name < results[j].Name
	})

	if results == nil {
		results = []PodInfo{}
	}

	return results, nil
}

// ListNamespaces returns active namespaces in the cluster.
func (m *K8sManager) ListNamespaces(ctx context.Context) ([]NamespaceInfo, error) {
	nsList, err := m.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	var results []NamespaceInfo
	for _, ns := range nsList.Items {
		results = append(results, NamespaceInfo{
			Name:      ns.Name,
			Status:    string(ns.Status.Phase),
			Age:       formatDuration(time.Since(ns.CreationTimestamp.Time)),
			CreatedAt: ns.CreationTimestamp.Time,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})

	if results == nil {
		results = []NamespaceInfo{}
	}

	return results, nil
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	return fmt.Sprintf("%dd", days)
}

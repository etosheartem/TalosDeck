package k8s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// podCache stores a short-lived cache of pods to prevent flooding the API server.
type podCache struct {
	sync.RWMutex
	pods      []PodInfo
	updatedAt time.Time
	key       string
	ttl       time.Duration
}

// K8sManager manages interaction with the Kubernetes cluster workloads.
type K8sManager struct {
	clientset kubernetes.Interface
	cache     podCache
}

// NewK8sManager initializes Kubernetes client using in-cluster config, kubeconfig file, or talos kubeconfig fallback.
func NewK8sManager(kubeconfigPath string, kubeconfigBytesProvider func(ctx context.Context) ([]byte, error)) (*K8sManager, error) {
	var restConfig *rest.Config
	var err error
	var loadErrHistory []string

	// 1. Try In-Cluster config if running inside a Kubernetes Pod
	if restConfig == nil {
		inClusterCfg, inClusterErr := rest.InClusterConfig()
		if inClusterErr == nil {
			restConfig = inClusterCfg
		} else {
			loadErrHistory = append(loadErrHistory, fmt.Sprintf("in-cluster: %v", inClusterErr))
		}
	}

	// 2. Try explicit or env KUBECONFIG
	if restConfig == nil {
		if kubeconfigPath == "" {
			kubeconfigPath = os.Getenv("KUBECONFIG")
		}

		candidatePaths := []string{}
		if kubeconfigPath != "" {
			candidatePaths = append(candidatePaths, kubeconfigPath)
		}

		// Standard local fallbacks
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" {
			candidatePaths = append(candidatePaths, filepath.Join(homeDir, ".kube", "config"))
		}
		candidatePaths = append(candidatePaths,
			"/home/artem/laba-kuber/kubeconfig",
			"./kubeconfig",
			"../kubeconfig",
			"./cluster-config/kubeconfig",
		)

		for _, p := range candidatePaths {
			if p == "" {
				continue
			}
			if _, statErr := os.Stat(p); statErr == nil {
				cfg, bErr := clientcmd.BuildConfigFromFlags("", p)
				if bErr == nil {
					restConfig = cfg
					break
				} else {
					loadErrHistory = append(loadErrHistory, fmt.Sprintf("file %s: %v", p, bErr))
				}
			}
		}
	}

	// 3. Fallback to Talos-provided dynamic kubeconfig
	if restConfig == nil && kubeconfigBytesProvider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		rawBytes, kErr := kubeconfigBytesProvider(ctx)
		if kErr == nil && len(rawBytes) > 0 {
			cfg, rErr := clientcmd.RESTConfigFromKubeConfig(rawBytes)
			if rErr == nil {
				restConfig = cfg
			} else {
				loadErrHistory = append(loadErrHistory, fmt.Sprintf("talos provider parse: %v", rErr))
			}
		} else if kErr != nil {
			loadErrHistory = append(loadErrHistory, fmt.Sprintf("talos provider fetch: %v", kErr))
		}
	}

	if restConfig == nil {
		return nil, fmt.Errorf("no valid kubeconfig found (attempts: %s)", strings.Join(loadErrHistory, "; "))
	}

	// Set reasonable client timeouts
	restConfig.Timeout = 8 * time.Second

	cs, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	return &K8sManager{
		clientset: cs,
		cache: podCache{
			ttl: 2 * time.Second,
		},
	}, nil
}

// ListPods returns a summary list of pods matching the namespace and node filters.
func (m *K8sManager) ListPods(ctx context.Context, namespace, nodeFilter string) ([]PodInfo, error) {
	if m == nil || m.clientset == nil {
		return []PodInfo{}, fmt.Errorf("kubernetes client is not initialized")
	}

	queryNS := namespace
	if queryNS == "all" {
		queryNS = ""
	}

	if nodeFilter == "all" {
		nodeFilter = ""
	}

	cacheKey := fmt.Sprintf("%s|%s", queryNS, nodeFilter)

	// Check short-lived cache to protect etcd/apiserver from polling storms
	m.cache.RLock()
	if m.cache.key == cacheKey && time.Since(m.cache.updatedAt) < m.cache.ttl && len(m.cache.pods) > 0 {
		cached := make([]PodInfo, len(m.cache.pods))
		copy(cached, m.cache.pods)
		m.cache.RUnlock()
		return cached, nil
	}
	m.cache.RUnlock()

	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	listOpts := metav1.ListOptions{}
	if nodeFilter != "" {
		listOpts.FieldSelector = fmt.Sprintf("spec.nodeName=%s", nodeFilter)
	}

	podList, err := m.clientset.CoreV1().Pods(queryNS).List(reqCtx, listOpts)
	if err != nil {
		return []PodInfo{}, fmt.Errorf("failed to list pods: %w", err)
	}

	results := make([]PodInfo, 0, len(podList.Items))
	for _, p := range podList.Items {
		// Defensive fallback check in case API server does not support field selector for nodeName
		if nodeFilter != "" && p.Spec.NodeName != nodeFilter {
			continue
		}

		readyCount := 0
		restartCount := int32(0)
		status := string(p.Status.Phase)

		if p.DeletionTimestamp != nil {
			status = "Terminating"
		} else {
			// 1. Check init container statuses for crash/failure states
			for _, cs := range p.Status.InitContainerStatuses {
				restartCount += cs.RestartCount
				if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
					status = "Init:" + cs.State.Waiting.Reason
					break
				}
				if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
					if cs.State.Terminated.Reason != "" {
						status = "Init:" + cs.State.Terminated.Reason
					} else {
						status = fmt.Sprintf("Init:ExitCode:%d", cs.State.Terminated.ExitCode)
					}
					break
				}
			}

			// 2. Check main container statuses if not already flagged by init container
			if !strings.HasPrefix(status, "Init:") {
				for _, cs := range p.Status.ContainerStatuses {
					if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
						status = cs.State.Waiting.Reason
						break
					}
					if cs.State.Terminated != nil {
						if cs.State.Terminated.Reason != "" {
							status = cs.State.Terminated.Reason
							break
						}
						if cs.State.Terminated.ExitCode != 0 {
							status = fmt.Sprintf("Error:%d", cs.State.Terminated.ExitCode)
							break
						}
						if cs.State.Terminated.Signal != 0 {
							status = fmt.Sprintf("Signal:%d", cs.State.Terminated.Signal)
							break
						}
						if status == "" || status == "Running" {
							status = "Completed"
						}
					}
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

		podIP := p.Status.PodIP
		if podIP == "" && len(p.Status.PodIPs) > 0 {
			podIP = p.Status.PodIPs[0].IP
		}

		podID := string(p.UID)
		if podID == "" {
			podID = fmt.Sprintf("%s/%s", p.Namespace, p.Name)
		}

		results = append(results, PodInfo{
			ID:              podID,
			Name:            p.Name,
			Namespace:       p.Namespace,
			Node:            p.Spec.NodeName,
			NodeName:        p.Spec.NodeName,
			NodeIP:          p.Status.HostIP,
			Status:          status,
			Phase:           string(p.Status.Phase),
			ContainerCount:  countStr,
			ReadyContainers: countStr, // Compatible string e.g. "1/1"
			ReadyCount:      readyCount,
			TotalContainers: totalContainers,
			Restarts:        restartCount,
			Age:             ageStr,
			CreatedAt:       p.CreationTimestamp.Time,
			PodIP:           podIP,
			IP:              podIP,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Namespace != results[j].Namespace {
			return results[i].Namespace < results[j].Namespace
		}
		return results[i].Name < results[j].Name
	})

	// Store in cache
	m.cache.Lock()
	m.cache.key = cacheKey
	m.cache.pods = results
	m.cache.updatedAt = time.Now()
	m.cache.Unlock()

	return results, nil
}

// ListNamespaces returns active namespaces in the cluster.
func (m *K8sManager) ListNamespaces(ctx context.Context) ([]NamespaceInfo, error) {
	if m == nil || m.clientset == nil {
		return []NamespaceInfo{}, fmt.Errorf("kubernetes client is not initialized")
	}

	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	nsList, err := m.clientset.CoreV1().Namespaces().List(reqCtx, metav1.ListOptions{})
	if err != nil {
		return []NamespaceInfo{}, fmt.Errorf("failed to list namespaces: %w", err)
	}

	results := make([]NamespaceInfo, 0, len(nsList.Items))
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

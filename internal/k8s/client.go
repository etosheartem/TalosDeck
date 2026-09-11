package k8s

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// podCache stores a short-lived cache of pods to prevent flooding the API server.
type podCache struct {
	sync.RWMutex
	entries map[string]podCacheEntry
	ttl     time.Duration
}

type podCacheEntry struct {
	pods      []PodInfo
	updatedAt time.Time
}

// podListPageSize bounds a single List response so large clusters are fetched
// incrementally instead of in one memory spike.
const podListPageSize = 500

// K8sManager manages interaction with the Kubernetes cluster workloads.
type K8sManager struct {
	clientset kubernetes.Interface
	cache     podCache
	inflight  singleflight.Group

	// allowEmptyDirDeletion permits draining pods whose emptyDir data will be
	// destroyed. Off by default: losing data needs an explicit decision.
	allowEmptyDirDeletion bool
}

// SetAllowEmptyDirDeletion allows drain to evict pods backed by emptyDir volumes,
// accepting that their local data is destroyed.
func (m *K8sManager) SetAllowEmptyDirDeletion(allow bool) {
	if m != nil {
		m.allowEmptyDirDeletion = allow
	}
}

// statusPriority ranks pod states so a multi-container pod reports its most
// serious one, rather than whichever container happens to be listed first.
func statusPriority(status string) int {
	switch {
	case status == "":
		return 0
	case status == "Completed", status == "Running":
		return 1
	case strings.HasPrefix(status, "Init:"):
		return 3
	default:
		return 2
	}
}

// containerStateStatus renders a container state, or "" when it is unremarkable.
func containerStateStatus(state corev1.ContainerState) string {
	if state.Waiting != nil && state.Waiting.Reason != "" {
		return state.Waiting.Reason
	}
	if state.Terminated == nil {
		return ""
	}
	switch {
	case state.Terminated.Reason != "" && state.Terminated.Reason != "Completed":
		return state.Terminated.Reason
	case state.Terminated.ExitCode != 0:
		return fmt.Sprintf("Error:%d", state.Terminated.ExitCode)
	case state.Terminated.Signal != 0:
		return fmt.Sprintf("Signal:%d", state.Terminated.Signal)
	default:
		return "Completed"
	}
}

// podDisplayStatus derives the status shown in the UI, preferring the most severe
// signal across every container and surfacing the reason Kubernetes already knows.
func podDisplayStatus(p *corev1.Pod) string {
	if p.DeletionTimestamp != nil {
		return "Terminating"
	}

	status := string(p.Status.Phase)

	// Phase alone hides why: an evicted pod is "Failed", an unschedulable one is
	// a bare "Pending". The API already carries the reason — show it.
	if p.Status.Reason != "" {
		status = p.Status.Reason
	} else {
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason != "" {
				status = cond.Reason
				break
			}
		}
	}

	// Init containers gate the pod, so their failures outrank main containers.
	for _, cs := range p.Status.InitContainerStatuses {
		if s := containerStateStatus(cs.State); s != "" && s != "Completed" {
			candidate := "Init:" + s
			if statusPriority(candidate) >= statusPriority(status) {
				status = candidate
			}
		}
	}

	if !strings.HasPrefix(status, "Init:") {
		for _, cs := range p.Status.ContainerStatuses {
			s := containerStateStatus(cs.State)
			if s == "" {
				continue
			}
			// Never let a finished sidecar mask a sibling that is still crashing.
			if statusPriority(s) > statusPriority(status) {
				status = s
			}
		}
	}

	return status
}

// NewK8sManager initializes Kubernetes client using in-cluster config, kubeconfig file, or talos kubeconfig fallback.
func NewK8sManager(kubeconfigPath string, kubeconfigBytesProvider func(ctx context.Context) ([]byte, error)) (*K8sManager, error) {
	var restConfig *rest.Config
	var err error
	var loadErrHistory []string

	// 1. An explicitly selected kubeconfig is an instruction, not a hint: it wins
	// over in-cluster credentials and fails loudly instead of silently falling
	// through to some other cluster. Drain and maintenance act on real nodes.
	if kubeconfigPath != "" {
		cfg, bErr := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if bErr != nil {
			return nil, fmt.Errorf("failed to load explicitly configured kubeconfig %s: %w", kubeconfigPath, bErr)
		}
		restConfig = cfg
	}

	// 2. KUBECONFIG may list several files separated by the OS path separator;
	// merge them the way kubectl does rather than treating the list as one name.
	if restConfig == nil {
		if envPaths := os.Getenv("KUBECONFIG"); envPaths != "" {
			rules := &clientcmd.ClientConfigLoadingRules{Precedence: filepath.SplitList(envPaths)}
			cfg, bErr := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
			if bErr != nil {
				return nil, fmt.Errorf("failed to load kubeconfig from KUBECONFIG=%s: %w", envPaths, bErr)
			}
			restConfig = cfg
		}
	}

	// 3. Try In-Cluster config if running inside a Kubernetes Pod
	if restConfig == nil {
		inClusterCfg, inClusterErr := rest.InClusterConfig()
		if inClusterErr == nil {
			restConfig = inClusterCfg
		} else {
			loadErrHistory = append(loadErrHistory, fmt.Sprintf("in-cluster: %v", inClusterErr))
		}
	}

	// 4. Standard local fallbacks
	if restConfig == nil {
		candidatePaths := []string{}
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" {
			candidatePaths = append(candidatePaths, filepath.Join(homeDir, ".kube", "config"))
		}
		candidatePaths = append(candidatePaths,
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
				}
				loadErrHistory = append(loadErrHistory, fmt.Sprintf("file %s: %v", p, bErr))
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
			entries: make(map[string]podCacheEntry),
			ttl:     2 * time.Second,
		},
		allowEmptyDirDeletion: strings.EqualFold(os.Getenv("TALOSDECK_ALLOW_EMPTYDIR_DELETION"), "true"),
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
	entry, found := m.cache.entries[cacheKey]
	if found && time.Since(entry.updatedAt) < m.cache.ttl {
		cached := append([]PodInfo(nil), entry.pods...)
		m.cache.RUnlock()
		return cached, nil
	}
	m.cache.RUnlock()

	// A cache miss from N dashboard sessions must collapse into one API call,
	// otherwise the TTL expiry itself becomes the polling burst it guards against.
	shared, err, _ := m.inflight.Do(cacheKey, func() (any, error) {
		return m.listPodsUncached(ctx, queryNS, nodeFilter, cacheKey)
	})
	if err != nil {
		return []PodInfo{}, err
	}

	pods, _ := shared.([]PodInfo)
	return append([]PodInfo(nil), pods...), nil
}

// listPodsUncached performs the actual paginated API query and refreshes the cache.
func (m *K8sManager) listPodsUncached(ctx context.Context, queryNS, nodeFilter, cacheKey string) ([]PodInfo, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	listOpts := metav1.ListOptions{Limit: podListPageSize}
	if nodeFilter != "" {
		listOpts.FieldSelector = fmt.Sprintf("spec.nodeName=%s", nodeFilter)
	}

	// Page through the collection: a single unbounded List spikes memory on large
	// clusters and is far likelier to blow the request deadline.
	var items []corev1.Pod
	for {
		podList, err := m.clientset.CoreV1().Pods(queryNS).List(reqCtx, listOpts)
		if err != nil {
			return []PodInfo{}, fmt.Errorf("failed to list pods: %w", err)
		}

		items = append(items, podList.Items...)

		if podList.Continue == "" {
			break
		}
		listOpts.Continue = podList.Continue
	}

	results := make([]PodInfo, 0, len(items))
	for _, p := range items {
		// Defensive fallback check in case API server does not support field selector for nodeName
		if nodeFilter != "" && p.Spec.NodeName != nodeFilter {
			continue
		}

		readyCount := 0
		restartCount := int32(0)

		for _, cs := range p.Status.InitContainerStatuses {
			restartCount += cs.RestartCount
		}
		for _, cs := range p.Status.ContainerStatuses {
			restartCount += cs.RestartCount
			if cs.Ready {
				readyCount++
			}
		}

		status := podDisplayStatus(&p)

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
	if m.cache.entries == nil {
		m.cache.entries = make(map[string]podCacheEntry)
	}
	now := time.Now()
	for key, cached := range m.cache.entries {
		if now.Sub(cached.updatedAt) >= m.cache.ttl {
			delete(m.cache.entries, key)
		}
	}
	m.cache.entries[cacheKey] = podCacheEntry{
		pods:      append([]PodInfo(nil), results...),
		updatedAt: now,
	}
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

	results := make([]NamespaceInfo, 0)
	listOpts := metav1.ListOptions{Limit: podListPageSize}
	for {
		nsList, err := m.clientset.CoreV1().Namespaces().List(reqCtx, listOpts)
		if err != nil {
			return []NamespaceInfo{}, fmt.Errorf("failed to list namespaces: %w", err)
		}

		for _, ns := range nsList.Items {
			results = append(results, NamespaceInfo{
				Name:      ns.Name,
				Status:    string(ns.Status.Phase),
				Age:       formatDuration(time.Since(ns.CreationTimestamp.Time)),
				CreatedAt: ns.CreationTimestamp.Time,
			})
		}

		if nsList.Continue == "" {
			break
		}
		listOpts.Continue = nsList.Continue
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

// CordonAndDrainNode marks a Kubernetes node as unschedulable (cordon) and evicts/deletes non-daemonset pods (drain)
// prior to node decommissioning or VM deletion (PVE-07).
func (m *K8sManager) CordonAndDrainNode(ctx context.Context, nodeName string) error {
	if m == nil || m.clientset == nil {
		return fmt.Errorf("kubernetes client is not available")
	}

	nodeName = strings.TrimSpace(nodeName)
	if nodeName == "" {
		return fmt.Errorf("nodeName is required for cordon and drain")
	}

	resolvedName, err := m.SetNodeMaintenance(ctx, nodeName, true)
	if err != nil {
		return err
	}
	nodeName = resolvedName

	// 2. Drain: list pods scheduled on this node and evict/delete them
	pods, err := m.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return fmt.Errorf("failed to list pods on node %s for drain: %w", nodeName, err)
	}

	drainable := make([]corev1.Pod, 0, len(pods.Items))
	for _, pod := range pods.Items {
		if skipPodDuringDrain(pod) {
			continue
		}
		// A bare owner reference is not a controller: only a reference with
		// Controller=true guarantees something will recreate the pod elsewhere.
		if metav1.GetControllerOf(&pod) == nil {
			return fmt.Errorf("cannot safely drain node %s: pod %s/%s has no controller", nodeName, pod.Namespace, pod.Name)
		}
		// Evicting a pod with emptyDir destroys that data for good, so refuse by
		// default and make the operator opt in, the way `kubectl drain` does.
		if !m.allowEmptyDirDeletion {
			if vol := podEmptyDirVolume(&pod); vol != "" {
				return fmt.Errorf(
					"cannot safely drain node %s: pod %s/%s uses emptyDir volume %q whose data would be lost; set TALOSDECK_ALLOW_EMPTYDIR_DELETION=true to allow",
					nodeName, pod.Namespace, pod.Name, vol)
			}
		}
		drainable = append(drainable, pod)
	}

	for _, pod := range drainable {
		if err := m.evictPod(ctx, pod); err != nil {
			return fmt.Errorf("failed to evict pod %s/%s from node %s: %w", pod.Namespace, pod.Name, nodeName, err)
		}
	}

	// Eviction is asynchronous. Do not let the caller power off the VM until all
	// non-daemonset workloads have actually left the node.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		remaining, err := m.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
			FieldSelector: "spec.nodeName=" + nodeName,
		})
		if err != nil {
			return fmt.Errorf("failed to verify drain of node %s: %w", nodeName, err)
		}

		pending := 0
		for _, pod := range remaining.Items {
			if !skipPodDuringDrain(pod) {
				pending++
			}
		}
		if pending == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %d workload pod(s) to leave node %s: %w", pending, nodeName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// SetNodeMaintenance cordons or uncordons a node. The identifier may be either
// its Kubernetes name or one of its internal/external IP addresses.
func (m *K8sManager) SetNodeMaintenance(ctx context.Context, identifier string, enable bool) (string, error) {
	if m == nil || m.clientset == nil {
		return "", fmt.Errorf("kubernetes client is not available")
	}

	nodeName, err := m.resolveNodeName(ctx, strings.TrimSpace(identifier))
	if err != nil {
		return "", err
	}

	patch := []byte(fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, enable))
	if _, err := m.clientset.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		action := "uncordon"
		if enable {
			action = "cordon"
		}
		return "", fmt.Errorf("failed to %s node %s: %w", action, nodeName, err)
	}

	return nodeName, nil
}

func (m *K8sManager) resolveNodeName(ctx context.Context, identifier string) (string, error) {
	if identifier == "" {
		return "", fmt.Errorf("node identifier is required")
	}
	if net.ParseIP(identifier) == nil {
		if _, err := m.clientset.CoreV1().Nodes().Get(ctx, identifier, metav1.GetOptions{}); err != nil {
			return "", fmt.Errorf("failed to find Kubernetes node %s: %w", identifier, err)
		}
		return identifier, nil
	}

	nodes, err := m.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to resolve Kubernetes node IP %s: %w", identifier, err)
	}
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			if address.Address == identifier {
				return node.Name, nil
			}
		}
	}
	return "", fmt.Errorf("Kubernetes node with IP %s was not found", identifier)
}

func skipPodDuringDrain(pod corev1.Pod) bool {
	if _, mirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; mirror {
		return true
	}
	// Only the controlling reference decides: a plain reference naming some
	// DaemonSet must not exempt an ordinary workload pod from the drain.
	if owner := metav1.GetControllerOf(&pod); owner != nil && owner.Kind == "DaemonSet" {
		return true
	}
	return false
}

// podEmptyDirVolume returns the name of the first emptyDir volume, or "".
func podEmptyDirVolume(pod *corev1.Pod) string {
	for _, v := range pod.Spec.Volumes {
		if v.EmptyDir != nil {
			return v.Name
		}
	}
	return ""
}

// evictPod evicts one pod, honouring its own termination grace period and
// retrying while a PodDisruptionBudget temporarily forbids the disruption.
func (m *K8sManager) evictPod(ctx context.Context, pod corev1.Pod) error {
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
	}
	// A workload that asked for 120s to flush state must get it; forcing 30s here
	// truncated exactly the shutdown the pod author configured.
	if grace := pod.Spec.TerminationGracePeriodSeconds; grace != nil {
		eviction.DeleteOptions = &metav1.DeleteOptions{GracePeriodSeconds: grace}
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		err := m.clientset.PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction)
		switch {
		case err == nil, apierrors.IsNotFound(err):
			return nil
		case !apierrors.IsTooManyRequests(err):
			return err
		}

		// 429 means a PDB is momentarily blocking us — a rolling update, usually.
		// Keep trying until the caller's deadline instead of failing the drain.
		select {
		case <-ctx.Done():
			return fmt.Errorf("disruption budget still blocking eviction: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

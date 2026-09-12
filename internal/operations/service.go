package operations

import (
	"context"
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/compatibility"
	"talosdeck/internal/backup"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/talos"
)

type Talos interface {
	ListNodes(context.Context) ([]*talos.NodeOverview, error)
	GetEtcdStatus(context.Context) (*talos.EtcdClusterStatus, error)
	GetInstallerImage(context.Context, string) (string, error)
	GetConfigPath() string
	GetClusterName() string
}
type Kubernetes interface {
	UpgradeInventory(context.Context) (string, []k8s.UpgradeNode, error)
}
type componentVerifier interface {
	VerifyUpgradeComponents(context.Context, string, bool) error
}
type diskVerifier interface {
	CheckUpgradeDisk(context.Context, string) error
}
type Backups interface {
	CreateEtcdSnapshot(context.Context, string) (*backup.BackupInfo, error)
	VerifyBackup(string) (bool, string, error)
}
type Service struct {
	Talos        Talos
	Kubernetes   Kubernetes
	Backups      Backups
	CLI          CommandRunner
	PollInterval time.Duration
	Config       *ConfigService
	ClusterName  string
}

func (s *Service) ConfirmationName() string {
	if s.ClusterName != "" {
		return s.ClusterName
	}
	if unavailable(s.Talos) {
		return ""
	}
	return s.Talos.GetClusterName()
}

type Node struct {
	IP      string `json:"ip"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Current string `json:"current"`
	Image   string `json:"image,omitempty"`
	Skip    bool   `json:"skip"`
}
type Plan struct {
	Kind              string   `json:"kind"`
	Version           string   `json:"version,omitempty"`
	Nodes             []Node   `json:"nodes"`
	Warnings          []string `json:"warnings"`
	KubernetesVersion string   `json:"kubernetesVersion"`
}

var release = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

func parseVersion(s string) (semver.Version, error) {
	if !release.MatchString(s) {
		return semver.Version{}, fmt.Errorf("expected a stable version such as 1.14.1, got %q", s)
	}
	return semver.Parse(strings.TrimPrefix(s, "v"))
}
func Validate(r jobs.Request) error {
	if r.Node != "" || r.ConfigRevisionID != "" {
		return fmt.Errorf("configuration references are not accepted by upgrade endpoints")
	}
	switch r.Kind {
	case "talos-upgrade", "kubernetes-upgrade":
		_, err := parseVersion(r.Version)
		return err
	case "rolling-reboot":
		if r.Version != "" {
			return fmt.Errorf("reboot has no target version")
		}
		return nil
	default:
		return fmt.Errorf("unknown operation kind")
	}
}
func installerFor(current, target string) (string, error) {
	// Only change the tag on the existing repository/schematic. Digests and
	// missing tags require an explicit operator change to machine configuration.
	if strings.ContainsAny(current, " \t\r\n@") || strings.Contains(current, "://") {
		return "", fmt.Errorf("installer must be a tagged image, without a digest or URL")
	}
	colon := strings.LastIndex(current, ":")
	slash := strings.LastIndex(current, "/")
	if colon <= slash || slash < 1 {
		return "", fmt.Errorf("cannot safely derive installer from %q", current)
	}
	if _, err := parseVersion(current[colon+1:]); err != nil {
		return "", fmt.Errorf("installer tag is not a stable Talos version")
	}
	return current[:colon] + ":v" + strings.TrimPrefix(target, "v"), nil
}
func (s *Service) Preflight(ctx context.Context, r jobs.Request) (*Plan, error) {
	if err := Validate(r); err != nil {
		return nil, err
	}
	if unavailable(s.Talos) || unavailable(s.Kubernetes) || unavailable(s.Backups) || unavailable(s.CLI) {
		return nil, fmt.Errorf("Talos, Kubernetes, backup manager and talosctl are required")
	}
	if err := s.CLI.Check(ctx); err != nil {
		return nil, err
	}
	nodes, err := s.Talos.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no Talos nodes discovered")
	}
	apiVersion, knodes, err := s.Kubernetes.UpgradeInventory(ctx)
	if err != nil {
		return nil, fmt.Errorf("Kubernetes preflight: %w", err)
	}
	if len(nodes) != len(knodes) {
		return nil, fmt.Errorf("Talos inventory (%d) and Kubernetes inventory (%d) differ; configure every node before upgrading", len(nodes), len(knodes))
	}
	apiSem, err := parseVersion(apiVersion)
	if err != nil {
		return nil, err
	}
	if verifier, ok := s.Kubernetes.(componentVerifier); ok {
		if err := verifier.VerifyUpgradeComponents(ctx, apiVersion, false); err != nil {
			return nil, fmt.Errorf("Kubernetes component preflight: %w", err)
		}
	}
	plan := &Plan{Kind: r.Kind, Version: strings.TrimPrefix(r.Version, "v"), KubernetesVersion: apiVersion, Nodes: []Node{}, Warnings: []string{"An etcd snapshot will be created and verified before changes. This does not back up application volumes.", "If TalosDeck runs on a node being drained, the job may be interrupted. Run from an external management host for an uninterrupted upgrade."}}
	cp := 0
	changes := 0
	seen := map[string]bool{}
	for _, n := range nodes {
		if n == nil || !n.Ready {
			return nil, fmt.Errorf("every Talos node must be ready")
		}
		if n.Role != "controlplane" && n.Role != "worker" {
			return nil, fmt.Errorf("unknown role for %s", n.IP)
		}
		if n.Role == "controlplane" {
			cp++
		}
		if n.ServicesSummary == nil || n.ServicesSummary.Kubelet != "Healthy" || n.ServicesSummary.Containerd != "Healthy" || n.ServicesSummary.Apid != "Healthy" {
			return nil, fmt.Errorf("core services are not healthy on %s", n.IP)
		}
		var match *k8s.UpgradeNode
		for i := range knodes {
			for _, address := range knodes[i].Addresses {
				a, ae := netip.ParseAddr(address)
				b, be := netip.ParseAddr(n.IP)
				if ae == nil && be == nil && a.Unmap() == b.Unmap() {
					if match != nil && match.Name != knodes[i].Name {
						return nil, fmt.Errorf("ambiguous Kubernetes address %s", n.IP)
					}
					match = &knodes[i]
				}
			}
		}
		if match == nil || seen[match.Name] {
			return nil, fmt.Errorf("Talos node %s does not uniquely match this Kubernetes cluster", n.IP)
		}
		seen[match.Name] = true
		if !match.Ready || match.Unschedulable || match.Pressure {
			return nil, fmt.Errorf("Kubernetes node %s must be Ready and schedulable", match.Name)
		}
		if r.Kind != "rolling-reboot" {
			if verifier, ok := s.Talos.(diskVerifier); ok {
				if err := verifier.CheckUpgradeDisk(ctx, n.IP); err != nil {
					return nil, err
				}
			}
		}
		tv, err := parseVersion(n.Version)
		if err != nil {
			return nil, err
		}
		kv, err := parseVersion(match.Version)
		if err != nil {
			return nil, err
		}
		nt := Node{IP: n.IP, Name: match.Name, Role: n.Role, Current: n.Version}
		if r.Kind != "rolling-reboot" {
			target, _ := parseVersion(r.Version)
			current := tv
			if r.Kind == "kubernetes-upgrade" {
				current = kv
				nt.Current = match.Version
			}
			if target.LT(current) || target.Major != current.Major || target.Minor > current.Minor+1 {
				return nil, fmt.Errorf("unsupported upgrade %s -> %s on %s: downgrades and skipped minor versions are disabled", current, target, n.IP)
			}
			talosTarget := n.Version
			kubeTarget := apiVersion
			if r.Kind == "talos-upgrade" {
				talosTarget = r.Version
			} else {
				kubeTarget = r.Version
				if target.LT(apiSem) || target.Major != apiSem.Major || target.Minor > apiSem.Minor+1 {
					return nil, fmt.Errorf("unsupported Kubernetes API version upgrade %s -> %s", apiSem, target)
				}
			}
			compatTalos, err := compatibility.ParseTalosVersion(&machine.VersionInfo{Tag: talosTarget})
			if err != nil {
				return nil, err
			}
			compatKube, err := compatibility.ParseKubernetesVersion(kubeTarget)
			if err != nil {
				return nil, err
			}
			if err = compatKube.SupportedWith(compatTalos); err != nil {
				return nil, err
			}
			if r.Kind == "talos-upgrade" {
				host, err := compatibility.ParseTalosVersion(&machine.VersionInfo{Tag: n.Version})
				if err != nil {
					return nil, err
				}
				if err = compatTalos.UpgradeableFrom(host); err != nil {
					return nil, err
				}
				// Validate the kubelet as well as the apiserver against the target OS.
				kubelet, _ := compatibility.ParseKubernetesVersion(match.Version)
				if err = kubelet.SupportedWith(compatTalos); err != nil {
					return nil, err
				}
				image, err := s.Talos.GetInstallerImage(ctx, n.IP)
				if err != nil {
					return nil, err
				}
				nt.Image, err = installerFor(image, r.Version)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", n.IP, err)
				}
			}
			nt.Skip = target.EQ(current)
			if !nt.Skip {
				changes++
			}
		}
		plan.Nodes = append(plan.Nodes, nt)
	}
	if cp == 0 {
		return nil, fmt.Errorf("no control plane found")
	}
	if r.Kind != "kubernetes-upgrade" {
		if cp == 1 && !r.AllowDowntime {
			return nil, fmt.Errorf("single control plane: acknowledge API downtime before continuing")
		}
		if cp > 1 && cp-1 < cp/2+1 {
			return nil, fmt.Errorf("cannot take a control plane offline without losing etcd quorum")
		}
		if cp == 1 {
			plan.Warnings = append(plan.Warnings, "The single control plane will reboot: Kubernetes API downtime is expected.")
		}
	}
	etcd, err := s.Talos.GetEtcdStatus(ctx)
	if err != nil {
		return nil, err
	}
	if etcd == nil || !etcd.Healthy || len(etcd.Alarms) > 0 || len(etcd.Errors) > 0 || len(etcd.Members) != cp {
		return nil, fmt.Errorf("etcd must be healthy, alarm-free and match the control plane inventory")
	}
	for _, member := range etcd.Members {
		if !member.Healthy || member.IsLearner {
			return nil, fmt.Errorf("all etcd members must be healthy voting members")
		}
	}
	if r.Kind != "rolling-reboot" && changes == 0 && !(r.Kind == "kubernetes-upgrade" && apiSem.String() != plan.Version) {
		return nil, ErrNoChanges
	}
	sort.Slice(plan.Nodes, func(i, j int) bool {
		a, b := plan.Nodes[i], plan.Nodes[j]
		if a.Role != b.Role {
			if r.Kind == "rolling-reboot" {
				return a.Role == "worker"
			}
			return a.Role == "controlplane"
		}
		return a.IP < b.IP
	})
	return plan, nil
}
func (s *Service) args(node string) []string {
	return []string{"--talosconfig", s.Talos.GetConfigPath(), "--context", s.Talos.GetClusterName(), "--nodes", node}
}
func (s *Service) Run(ctx context.Context, e *jobs.Execution, r jobs.Request) error {
	if r.Kind == "config-apply" || r.Kind == "config-restore" {
		if s.Config == nil {
			return fmt.Errorf("configuration operations unavailable")
		}
		return s.Config.Run(ctx, e, r)
	}
	if err := e.Checkpoint(ctx, "preflight", "Checking cluster health, inventory and version compatibility"); err != nil {
		return err
	}
	plan, err := s.Preflight(ctx, r)
	if err != nil {
		return err
	}
	cp := ""
	for _, node := range plan.Nodes {
		if node.Role == "controlplane" {
			cp = node.IP
			break
		}
	}
	log := func(line string) error { return e.Log("command", line) }
	if r.Kind == "kubernetes-upgrade" {
		if err = e.Checkpoint(ctx, "dry-run", "Running official Kubernetes upgrade dry-run"); err != nil {
			return err
		}
		args := append(s.args(cp), "upgrade-k8s", "--to", plan.Version, "--dry-run")
		if err = s.CLI.Run(ctx, args, log); err != nil {
			return err
		}
	}
	if err = e.Checkpoint(ctx, "backup", "Creating pre-operation etcd snapshot"); err != nil {
		return err
	}
	snapshot, err := s.Backups.CreateEtcdSnapshot(ctx, cp)
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}
	if snapshot == nil {
		return fmt.Errorf("backup returned no metadata")
	}
	ok, _, err := s.Backups.VerifyBackup(snapshot.ID)
	if err != nil || !ok {
		return fmt.Errorf("backup verification failed: %v", err)
	}
	if err = e.Log("backup", "Verified etcd snapshot: "+snapshot.ID); err != nil {
		return err
	}
	if r.Kind == "kubernetes-upgrade" {
		// Recheck after the dry-run and backup, immediately before the mutation.
		if _, err = s.Preflight(ctx, r); err != nil {
			return err
		}
		if err = e.Checkpoint(ctx, "kubernetes-upgrade", "Updating control plane, kube-proxy and kubelets using talosctl"); err != nil {
			return err
		}
		if err = s.CLI.Run(ctx, append(s.args(cp), "upgrade-k8s", "--to", plan.Version), log); err != nil {
			return fmt.Errorf("%w: %v", jobs.ErrUncertain, err)
		}
		if err = s.wait(ctx, func(c context.Context) bool {
			v, nodes, err := s.Kubernetes.UpgradeInventory(c)
			if err != nil || strings.TrimPrefix(v, "v") != plan.Version || len(nodes) != len(plan.Nodes) {
				return false
			}
			for _, n := range nodes {
				if !n.Ready || n.Unschedulable || n.Pressure || strings.TrimPrefix(n.Version, "v") != plan.Version {
					return false
				}
			}
			if verifier, ok := s.Kubernetes.(componentVerifier); ok {
				if verifier.VerifyUpgradeComponents(c, plan.Version, true) != nil {
					return false
				}
			}
			return true
		}); err != nil {
			return err
		}
	} else {
		for _, node := range plan.Nodes {
			if node.Skip {
				if err = e.Log("skip", node.Name+" already has the target version"); err != nil {
					return err
				}
				continue
			}
			// Readiness and quorum must still hold before each machine is touched.
			if _, err = s.Preflight(ctx, r); err != nil {
				return err
			}
			if err = e.Checkpoint(ctx, "node", fmt.Sprintf("%s: %s", r.Kind, node.Name)); err != nil {
				return err
			}
			args := s.args(node.IP)
			if r.Kind == "talos-upgrade" {
				args = append(args, "upgrade", "--image", node.Image, "--drain", "--wait", "--timeout", "20m", "--progress", "plain")
			} else {
				args = append(args, "reboot", "--drain", "--wait", "--timeout", "20m", "--progress", "plain")
			}
			if err = s.CLI.Run(ctx, args, log); err != nil {
				return fmt.Errorf("%s: %w: %v", node.Name, jobs.ErrUncertain, err)
			}
			if err = s.wait(ctx, func(c context.Context) bool {
				_, knodes, kerr := s.Kubernetes.UpgradeInventory(c)
				if kerr != nil {
					return false
				}
				kready := false
				for _, n := range knodes {
					if n.Name == node.Name {
						kready = n.Ready && !n.Unschedulable && !n.Pressure
					}
				}
				if !kready {
					return false
				}
				nodes, err := s.Talos.ListNodes(c)
				if err != nil {
					return false
				}
				for _, n := range nodes {
					if n.IP == node.IP {
						return n.Ready && (r.Kind == "rolling-reboot" || strings.TrimPrefix(n.Version, "v") == plan.Version)
					}
				}
				return false
			}); err != nil {
				return fmt.Errorf("%s did not become ready at the target version: %w", node.Name, err)
			}
			if err = e.Log("verified", node.Name+" is ready"); err != nil {
				return err
			}
		}
	}
	// Don't treat an accepted command as proof of completion.
	etcd, err := s.Talos.GetEtcdStatus(ctx)
	if err != nil {
		return err
	}
	if etcd == nil || !etcd.Healthy || len(etcd.Alarms) != 0 || len(etcd.Errors) != 0 {
		return fmt.Errorf("post-operation etcd health check failed")
	}
	controlPlanes := 0
	for _, node := range plan.Nodes {
		if node.Role == "controlplane" {
			controlPlanes++
		}
	}
	if len(etcd.Members) != controlPlanes {
		return fmt.Errorf("post-operation etcd member count changed")
	}
	for _, member := range etcd.Members {
		if !member.Healthy || member.IsLearner {
			return fmt.Errorf("post-operation etcd member health check failed")
		}
	}
	_, finalNodes, err := s.Kubernetes.UpgradeInventory(ctx)
	if err != nil || len(finalNodes) != len(plan.Nodes) {
		return fmt.Errorf("post-operation Kubernetes inventory check failed")
	}
	expected := map[string]bool{}
	for _, node := range plan.Nodes {
		expected[node.Name] = true
	}
	for _, node := range finalNodes {
		if !expected[node.Name] || !node.Ready || node.Unschedulable || node.Pressure {
			return fmt.Errorf("post-operation Kubernetes readiness check failed")
		}
		delete(expected, node.Name)
	}
	finalTalos, err := s.Talos.ListNodes(ctx)
	if err != nil || len(finalTalos) != len(plan.Nodes) {
		return fmt.Errorf("post-operation Talos inventory check failed")
	}
	byIP := map[string]bool{}
	for _, node := range plan.Nodes {
		byIP[node.IP] = true
	}
	for _, node := range finalTalos {
		if node == nil || !byIP[node.IP] || !node.Ready || node.ServicesSummary == nil || node.ServicesSummary.Kubelet != "Healthy" || node.ServicesSummary.Containerd != "Healthy" || node.ServicesSummary.Apid != "Healthy" {
			return fmt.Errorf("post-operation Talos health check failed")
		}
		if r.Kind == "talos-upgrade" && strings.TrimPrefix(node.Version, "v") != plan.Version {
			return fmt.Errorf("post-operation Talos version check failed")
		}
		delete(byIP, node.IP)
	}
	return e.Log("complete", "Operation completed and cluster health verified")
}

func unavailable(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func:
		return v.IsNil()
	}
	return false
}
func (s *Service) wait(ctx context.Context, ready func(context.Context) bool) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	interval := s.PollInterval
	if interval == 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ready(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

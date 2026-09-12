package operations

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"talosdeck/internal/backup"
	"talosdeck/internal/clusters"
	"talosdeck/internal/jobs"
	"talosdeck/internal/k8s"
	"talosdeck/internal/talos"
)

type DiagnosticCheck struct {
	ID         string `json:"id"`
	Severity   string `json:"severity"`
	Component  string `json:"component"`
	Node       string `json:"node,omitempty"`
	Title      string `json:"title"`
	Details    string `json:"details"`
	Suggestion string `json:"suggestion,omitempty"`
}
type DiagnosticSummary struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}
type DiagnosticReport struct {
	Status    string            `json:"status"`
	CheckedAt time.Time         `json:"checkedAt"`
	Checks    []DiagnosticCheck `json:"checks"`
	Summary   DiagnosticSummary `json:"summary"`
}
type DiagnosticsService struct {
	ClusterID  string
	Store      ProvisionStore
	Talos      *talos.TalosManager
	Kubernetes *k8s.K8sManager
	Backups    *BackupService
}

func (s *DiagnosticsService) Latest(ctx context.Context) (*DiagnosticReport, error) {
	r := &DiagnosticReport{Status: "unknown", Checks: []DiagnosticCheck{}}
	data, err := s.Store.GetSecret(ctx, s.ClusterID, "diagnostics", "latest")
	if errors.Is(err, clusters.ErrNotFound) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, r); err != nil {
		return nil, err
	}
	return r, nil
}
func (s *DiagnosticsService) Run(ctx context.Context, e *jobs.Execution, _ jobs.Request) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	r := &DiagnosticReport{Status: "healthy", CheckedAt: time.Now().UTC(), Checks: []DiagnosticCheck{}}
	defer func() {
		if result != nil {
			r.Checks = append(r.Checks, DiagnosticCheck{ID: "incomplete", Severity: "warning", Component: "diagnostics", Title: "Inspection incomplete", Details: "The operation ended before every check completed. Earlier findings are retained."})
			r.Summary.Warning++
		}
		if r.Summary.Critical > 0 {
			r.Status = "critical"
		} else if r.Summary.Warning > 0 {
			r.Status = "degraded"
		}
		data, err := json.Marshal(r)
		if err == nil {
			saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = s.Store.PutSecret(saveCtx, s.ClusterID, "diagnostics", "latest", data)
		}
		if result == nil {
			result = err
		}
	}()
	add := func(severity, component, node, title, details, suggestion string) {
		r.Checks = append(r.Checks, DiagnosticCheck{ID: fmt.Sprintf("check-%d", len(r.Checks)+1), Severity: severity, Component: component, Node: node, Title: title, Details: details, Suggestion: suggestion})
		switch severity {
		case "critical":
			r.Summary.Critical++
		case "warning":
			r.Summary.Warning++
		default:
			r.Summary.Info++
		}
	}
	step := func(name string) error { return e.Checkpoint(ctx, name, "Inspecting "+name) }
	if err := step("talos"); err != nil {
		return err
	}
	nodes, err := s.Talos.ListNodes(ctx)
	if err != nil || len(nodes) == 0 {
		add("critical", "talos", "", "Talos inventory unavailable", "No complete Talos inventory was returned.", "Check API endpoints, mTLS credentials and connectivity.")
	}
	for _, n := range nodes {
		if !n.Ready {
			add("critical", "talos", n.IP, "Node not ready", "Talos reports this node as unavailable or not ready.", "Inspect services, boot status and network connectivity.")
		}
		services, se := s.Talos.ListServices(ctx, n.IP)
		if se != nil {
			add("warning", "services", n.IP, "Services unavailable", "Service inspection failed.", "Check the Talos API and node state.")
		} else {
			for _, svc := range services {
				if (svc.HealthKnown && !svc.Healthy && svc.State == "Running") || svc.State == "Failed" || svc.State == "Crashed" {
					add("warning", "services", n.IP, "Unhealthy service: "+svc.ID, "State: "+svc.State, "Inspect this service's logs before restarting it.")
				}
			}
		}
		disks, de := s.Talos.GetNodeDisks(ctx, n.IP)
		if de != nil {
			add("warning", "storage", n.IP, "Disk inspection unavailable", "Filesystem usage could not be retrieved.", "Inspect Talos storage resources.")
		} else {
			for _, d := range disks {
				for _, p := range d.Partitions {
					if p.UsedPercent >= 85 {
						severity := "warning"
						if p.UsedPercent >= 95 {
							severity = "critical"
						}
						add(severity, "storage", n.IP, "Filesystem nearly full", fmt.Sprintf("%s: %d%% used", p.MountPath, p.UsedPercent), "Free space or expand the filesystem; inspect container images and logs.")
					}
				}
			}
		}
	}
	if err := step("etcd"); err != nil {
		return err
	}
	etcd, err := s.Talos.GetEtcdStatus(ctx)
	if err != nil || etcd == nil {
		add("critical", "etcd", "", "Etcd health unavailable", "Could not verify members, leader and alarms.", "Check every control plane through Talos API.")
	} else if !etcd.Healthy {
		add("critical", "etcd", "", "Etcd is degraded", fmt.Sprintf("Members: %d; alarms: %d", len(etcd.Members), len(etcd.Alarms)), "Restore quorum before maintenance or upgrades.")
	} else {
		add("info", "etcd", "", "Etcd healthy", fmt.Sprintf("%d members; no reported quorum failure", len(etcd.Members)), "")
	}
	if err := step("kubernetes"); err != nil {
		return err
	}
	version, knodes, err := s.Kubernetes.UpgradeInventory(ctx)
	if err != nil {
		add("critical", "kubernetes", "", "Kubernetes API unavailable", "Node inventory could not be read.", "Inspect apiserver, network access and kubeconfig.")
	} else {
		add("info", "kubernetes", "", "Kubernetes API reachable", "Version "+version, "")
		for _, n := range knodes {
			if !n.Ready {
				add("critical", "kubelet", n.Name, "Kubernetes node NotReady", "The Ready condition is false or unknown.", "Inspect kubelet, CNI and node pressure.")
			}
			if n.Pressure {
				add("warning", "pressure", n.Name, "Node pressure detected", "Disk, memory or PID pressure is reported.", "Inspect node resources and evictable workloads.")
			}
			if n.Unschedulable {
				add("info", "scheduling", n.Name, "Node cordoned", "Scheduling is disabled.", "Uncordon after maintenance completes.")
			}
		}
	}
	pods, err := s.Kubernetes.ListPods(ctx, "", "")
	if err != nil {
		add("warning", "workloads", "", "Pod inspection unavailable", "Could not list pods.", "Check Kubernetes permissions and API health.")
	} else {
		for _, p := range pods {
			if podNeedsDiagnosticAttention(p) {
				add("warning", "workloads", p.NodeName, "Pod requires attention", p.Namespace+"/"+p.Name+": "+p.Status, "Open the pod inspector for events and container status.")
			}
			if p.Restarts >= 5 {
				add("warning", "workloads", p.NodeName, "Repeated container restarts", fmt.Sprintf("%s/%s: %d restarts", p.Namespace, p.Name, p.Restarts), "Inspect termination reasons and recent logs.")
			}
		}
	}
	workloads, err := s.Kubernetes.Workloads(ctx, "kube-system")
	if err != nil {
		add("warning", "cni", "", "System workload inspection unavailable", "Could not inspect CNI and control-plane workloads.", "Verify API access.")
	} else {
		for _, w := range append(workloads.Deployments, workloads.DaemonSets...) {
			if w.Ready < w.Desired {
				add("warning", "cni", "", "System workload degraded", fmt.Sprintf("%s: %d/%d ready", w.Name, w.Ready, w.Desired), "Inspect system pod events, CNI and image pulls.")
			}
		}
	}
	if err := step("storage"); err != nil {
		return err
	}
	events, eventErr := s.Kubernetes.Events(ctx, "", "")
	if eventErr != nil {
		add("warning", "events", "", "Events unavailable", "Kubernetes events could not be read.", "Check API connectivity and permissions.")
	} else {
		count := 0
		for _, event := range events {
			if event.Type == "Warning" && time.Since(event.Time) < time.Hour {
				add("warning", "events", "", "Recent Kubernetes warning", event.Namespace+"/"+event.Name, "Open Events to inspect the message and affected resource.")
				count++
				if count >= 30 {
					break
				}
			}
		}
	}
	storage, err := s.Kubernetes.Storage(ctx)
	if err != nil {
		add("warning", "storage", "", "Volume inspection unavailable", "Could not read PV, PVC or StorageClass resources.", "Check Kubernetes storage permissions.")
	} else {
		for _, v := range storage.PersistentVolumeClaims {
			if v.Status != "Bound" {
				add("warning", "storage", "", "Unbound claim", v.Namespace+"/"+v.Name+": "+v.Status, "Inspect provisioner, StorageClass and claim events.")
			}
		}
	}
	if s.Backups != nil {
		backups, be := s.Backups.List(ctx)
		if be != nil || len(backups) == 0 {
			add("warning", "backups", "", "No verified restore point listed", "The backup catalog is empty or unavailable.", "Create a backup and test recovery.")
		} else {
			complete := latestCompleteBackup(backups)
			if backups[0].Partial {
				add("warning", "backups", "", "Latest backup is partial", "Some machine configurations are missing from the latest archive.", "Check the failed backup job and keep independent machine configurations before recovery.")
			}
			if complete == nil {
				add("warning", "backups", "", "No complete backup listed", "Only partial archives are available.", "Resolve unavailable machine configurations and create a complete backup.")
			} else if time.Since(complete.Timestamp) > 24*time.Hour {
				add("warning", "backups", "", "Latest complete backup is older than 24 hours", complete.Timestamp.UTC().Format(time.RFC3339), "Check schedule and recent backup jobs.")
			}
		}
	}
	if r.Summary.Critical > 0 {
		r.Status = "critical"
	} else if r.Summary.Warning > 0 {
		r.Status = "degraded"
	}
	return e.Log("complete", fmt.Sprintf("Diagnostics complete: %d critical, %d warnings", r.Summary.Critical, r.Summary.Warning))
}

func latestCompleteBackup(backups []*backup.BackupInfo) *backup.BackupInfo {
	var latest *backup.BackupInfo
	for _, candidate := range backups {
		if candidate != nil && !candidate.Partial && (latest == nil || candidate.Timestamp.After(latest.Timestamp)) {
			latest = candidate
		}
	}
	return latest
}

func podNeedsDiagnosticAttention(p k8s.PodInfo) bool {
	if p.Status == "Succeeded" || p.Status == "Completed" {
		return false
	}
	return p.Status != "Running" || (p.TotalContainers > 0 && p.ReadyCount < p.TotalContainers)
}

// Bundle uses only allowlisted diagnostic DTOs. It deliberately excludes raw
// logs/events, configs, environment values, Kubernetes Secrets and credentials.
func (s *DiagnosticsService) Bundle(ctx context.Context) ([]byte, error) {
	report, err := s.Latest(ctx)
	if err != nil {
		return nil, err
	}
	if report.Status == "unknown" {
		return nil, errors.New("run diagnostics before exporting a support bundle")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	files := map[string][]byte{"diagnostics.json": data, "README.txt": []byte("TalosDeck diagnostic export\nContains generated health findings only. Raw logs, event messages, configurations, private keys, tokens and Kubernetes Secret objects are excluded. Node names and internal addresses remain for troubleshooting.\n")}
	for name, body := range files {
		if strings.Contains(name, "/") {
			return nil, errors.New("invalid bundle filename")
		}
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))}); err != nil {
			return nil, err
		}
		if _, err = tw.Write(body); err != nil {
			return nil, err
		}
	}
	if err = tw.Close(); err != nil {
		return nil, err
	}
	if err = gz.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

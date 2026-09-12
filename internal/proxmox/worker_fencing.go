package proxmox

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"talosdeck/internal/reconcile"
	"time"
)

const WorkerFencingTTL = 60 * time.Second

var ErrFencingUnknown = errors.New("provider fencing state unknown; destructive continuation blocked")
var ErrFencingBlocked = errors.New("worker fencing blocked by ownership, HA or resource identity")

type WorkerFencingEvidence struct {
	Version                 int       `json:"version"`
	Mode                    string    `json:"mode"`
	Outcome                 string    `json:"outcome"`
	VerifiedAt              time.Time `json:"verifiedAt"`
	ProviderID              string    `json:"providerId"`
	ProviderResourceID      string    `json:"providerResourceId"`
	MachineID               string    `json:"machineId"`
	Generation              string    `json:"generation"` // Application machine UUID; NOT provider fencing epoch.
	DeletionTaskID          string    `json:"deletionTaskId,omitempty"`
	ResourceConfirmedAbsent bool      `json:"resourceConfirmedAbsent"`
	HAConfirmedAbsent       bool      `json:"haConfirmedAbsent"`
	Source                  string    `json:"source"`
}
type WorkerFencingProvider interface {
	DestroyOwnedForFence(context.Context, OwnedMachine) (WorkerFencingEvidence, error)
	RevalidateWorkerFence(context.Context, OwnedMachine, WorkerFencingEvidence) (WorkerFencingEvidence, error)
}

func newWorkerFence(m OwnedMachine) WorkerFencingEvidence {
	return WorkerFencingEvidence{Version: 1, Mode: "destroy-owned", Outcome: "UNKNOWN", ProviderID: m.ProviderID, ProviderResourceID: m.ProviderNode + "/" + strconv.Itoa(m.VMID), MachineID: m.ID, Generation: m.ID}
}

// DestroyOwnedForFence is destructive. Caller must first persist an intent and
// obtain explicit storage-impact and destruction confirmation. Stopping alone is
// not supported as fencing: it cannot prevent the old VM from returning.
func (c *Client) DestroyOwnedForFence(ctx context.Context, m OwnedMachine) (WorkerFencingEvidence, error) {
	evidence := newWorkerFence(m)
	if m.Role != "worker" || m.ID == "" || m.ProviderID == "" || m.ProviderNode != c.cfg.Node {
		return evidence, ErrFencingBlocked
	}
	if err := c.VerifyOwned(ctx, m); err != nil {
		return evidence, ErrFencingUnknown
	}
	if err := c.workerNotHA(ctx, m); err != nil {
		return evidence, err
	}
	// Establish sufficient read access before making an irreversible change.
	if err := c.workerAuditPermission(ctx, m); err != nil {
		return evidence, err
	}
	status, err := c.GetVMStatus(ctx, m.VMID)
	if err != nil {
		return evidence, ErrFencingUnknown
	}
	if status.Status == "running" {
		if err = reconcile.CheckRequiredMutation(ctx); err != nil {
			return evidence, ErrFencingUnknown
		}
		if err = c.StopVM(ctx, m.VMID); err != nil {
			return evidence, ErrFencingUnknown
		}
	} else if status.Status != "stopped" {
		return evidence, ErrFencingUnknown
	}
	if err = c.VerifyOwned(ctx, m); err != nil {
		return evidence, ErrFencingUnknown
	}
	if err = c.workerNotHA(ctx, m); err != nil {
		return evidence, err
	}
	var response struct {
		Data string `json:"data"`
	}
	if err = reconcile.CheckRequiredMutation(ctx); err != nil {
		return evidence, ErrFencingUnknown
	}
	if err = c.deleteJSON(ctx, fmt.Sprintf("/nodes/%s/qemu/%d?purge=1", url.PathEscape(m.ProviderNode), m.VMID), &response); err != nil {
		return evidence, ErrFencingUnknown
	}
	evidence.DeletionTaskID = response.Data
	if !matchingDeleteTask(response.Data, m) {
		return evidence, ErrFencingUnknown
	}
	if err = c.WaitTask(ctx, response.Data); err != nil {
		return evidence, ErrFencingUnknown
	}
	return c.proveWorkerFence(ctx, m, evidence)
}

// RevalidateWorkerFence never mutates the provider. Expired evidence causes fresh
// verification, never a repeat delete. A reused VMID always blocks continuation.
func (c *Client) RevalidateWorkerFence(ctx context.Context, m OwnedMachine, evidence WorkerFencingEvidence) (WorkerFencingEvidence, error) {
	expected := newWorkerFence(m)
	if evidence.Version != 1 || evidence.Mode != expected.Mode || evidence.ProviderID != expected.ProviderID || evidence.ProviderResourceID != expected.ProviderResourceID || evidence.MachineID != expected.MachineID || evidence.Generation != expected.Generation || m.Role != "worker" || m.ProviderNode != c.cfg.Node {
		return expected, ErrFencingBlocked
	}
	if !matchingDeleteTask(evidence.DeletionTaskID, m) {
		return expected, ErrFencingUnknown
	}
	return c.proveWorkerFence(ctx, m, evidence)
}
func (e WorkerFencingEvidence) Fresh(now time.Time) bool {
	return e.Version == 1 && e.Mode == "destroy-owned" && e.Outcome == "fenced" && e.ResourceConfirmedAbsent && e.HAConfirmedAbsent && !e.VerifiedAt.IsZero() && !e.VerifiedAt.After(now) && now.Sub(e.VerifiedAt) <= WorkerFencingTTL
}
func matchingDeleteTask(task string, m OwnedMachine) bool {
	p := strings.Split(task, ":")
	return len(p) == 9 && p[0] == "UPID" && p[1] == m.ProviderNode && p[5] == "qmdestroy" && p[6] == strconv.Itoa(m.VMID)
}
func (c *Client) proveWorkerFence(ctx context.Context, m OwnedMachine, evidence WorkerFencingEvidence) (WorkerFencingEvidence, error) {
	evidence.Outcome = "UNKNOWN"
	evidence.VerifiedAt = time.Time{}
	evidence.ResourceConfirmedAbsent = false
	evidence.HAConfirmedAbsent = false
	evidence.Source = ""
	task, err := c.GetTaskStatus(ctx, evidence.DeletionTaskID)
	if err != nil || task.Status != "stopped" || task.ExitStatus != "OK" || task.UPID != evidence.DeletionTaskID || task.Node != m.ProviderNode || task.Type != "qmdestroy" {
		return evidence, ErrFencingUnknown
	}
	if err = c.workerNotHA(ctx, m); err != nil {
		return evidence, err
	}
	if err = c.workerAuditPermission(ctx, m); err != nil {
		return evidence, err
	}
	var inventory struct {
		Data *[]struct {
			VMID int    `json:"vmid"`
			Type string `json:"type"`
		} `json:"data"`
	}
	if err = c.getJSON(ctx, "/cluster/resources?type=vm", &inventory); err != nil || inventory.Data == nil {
		return evidence, ErrFencingUnknown
	}
	for _, entry := range *inventory.Data {
		if entry.VMID <= 0 || (entry.Type != "qemu" && entry.Type != "lxc") {
			return evidence, ErrFencingUnknown
		}
		if entry.VMID == m.VMID {
			return evidence, ErrFencingBlocked
		}
	}
	// PVE filters VM inventory by this exact permission. Verify both sides of the
	// read; 403/empty filtered inventory alone never proves resource absence.
	if err = c.workerAuditPermission(ctx, m); err != nil {
		return evidence, err
	}
	evidence.Outcome = "fenced"
	evidence.VerifiedAt = time.Now().UTC()
	evidence.ResourceConfirmedAbsent = true
	evidence.HAConfirmedAbsent = true
	evidence.Source = "completed-delete-task+vm-audit-inventory"
	return evidence, nil
}
func (c *Client) workerAuditPermission(ctx context.Context, m OwnedMachine) error {
	path := "/vms/" + strconv.Itoa(m.VMID)
	var response struct {
		Data map[string]map[string]int `json:"data"`
	}
	if err := c.getJSON(ctx, "/access/permissions?path="+url.QueryEscape(path), &response); err != nil || response.Data[path]["VM.Audit"] != 1 {
		return ErrFencingUnknown
	}
	return nil
}
func (c *Client) workerNotHA(ctx context.Context, m OwnedMachine) error {
	var response struct {
		Data *[]struct {
			SID string `json:"sid"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/cluster/ha/resources", &response); err != nil || response.Data == nil {
		return ErrFencingUnknown
	}
	for _, entry := range *response.Data {
		if entry.SID == "" {
			return ErrFencingUnknown
		}
		if entry.SID == "vm:"+strconv.Itoa(m.VMID) || entry.SID == "ct:"+strconv.Itoa(m.VMID) {
			return ErrFencingBlocked
		}
	}
	return nil
}

package operations

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"talosdeck/internal/jobs"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/reconcile"
)

// Generation is the application-owned machine UUID, verified through Proxmox
// SMBIOS + secret ownership marker + MAC. It is NOT a provider fencing epoch.
// The ownership secret is deliberately never copied into the plaintext journal.
func machineIntentIdentity(record proxmox.OwnedMachineRecord) reconcile.Identity {
	return reconcile.Identity{ProviderID: record.ProviderID, ResourceID: record.ProviderNode + "/" + strconv.Itoa(record.VMID), Generation: record.ID, OwnerID: record.ClusterID}
}
func beginMachineIntent(ctx context.Context, e *jobs.Execution, action string, record proxmox.OwnedMachineRecord) error {
	return e.BeginIntent(ctx, action+":"+record.ID, action, machineIntentIdentity(record))
}
func proveMachineIntent(ctx context.Context, e *jobs.Execution, action string, record proxmox.OwnedMachineRecord) error {
	state := "exists"
	source := "provider_ownership_verified"
	if action == "delete" {
		state = "absent"
		source = "provider_delete_task_completed"
	}
	outcome, err := e.ObserveIntent(ctx, action+":"+record.ID, reconcile.Observation{Source: source, State: state, Identity: machineIntentIdentity(record), ObservedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	if outcome != "succeeded" {
		return fmt.Errorf("%w: provider result requires review", jobs.ErrUncertain)
	}
	return nil
}

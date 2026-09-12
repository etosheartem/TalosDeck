package operations

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/siderolabs/talos/pkg/machinery/client/config"
	"talosdeck/internal/clusters"
	"talosdeck/internal/jobs"
	"talosdeck/internal/proxmox"
)

// ReconcileInterrupted runs only after acquiring the job manager's exclusive
// startup lock. It never resumes infrastructure mutations.
func (s *ProvisionService) ReconcileInterrupted(ctx context.Context) error {
	ids, err := s.Store.ListSecretKeys(ctx, FleetScope, "provision-plan")
	if err != nil {
		return err
	}
	for _, id := range ids {
		data, err := s.Store.GetSecret(ctx, FleetScope, "provision-plan", id)
		var p provisionState
		if err != nil || json.Unmarshal(data, &p) != nil {
			return errors.New("cannot reconcile provisioning journal")
		}
		if p.ClusterID == s.ClusterID && p.Status == "running" {
			p.Status = "interrupted"
			if err := s.save(ctx, &p, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *ProvisionService) cleanupRecord(ctx context.Context, spec ProvisionSpec) (proxmox.OwnedMachineRecord, error) {
	r, err := loadOwned(ctx, s.Store, spec.MachineID)
	if err != nil || s.ClusterID != FleetScope || r.ClusterID != FleetScope || r.Status == "ready" || r.Status == "deleted" || r.Name != spec.Name {
		return r, errors.New("cleanup is limited to unregistered machines from failed provisioning")
	}
	data, err := s.Store.GetSecret(ctx, FleetScope, "provision-plan", r.PlanID)
	var source provisionState
	if err != nil || json.Unmarshal(data, &source) != nil || (source.Status != "failed" && source.Status != "interrupted") || source.ClusterID != FleetScope || source.ImportStarted {
		return r, errors.New("source provisioning plan must be failed or interrupted")
	}
	found := false
	for _, id := range source.MachineIDs {
		found = found || id == r.ID
	}
	if !found {
		return r, errors.New("machine is not owned by the failed plan")
	}
	if len(source.Talosconfig) > 0 {
		registry, ok := s.Store.(interface {
			List(context.Context) ([]clusters.Cluster, error)
		})
		if !ok {
			return r, errors.New("cannot verify cluster registry before cleanup")
		}
		cfg, err := config.FromBytes(source.Talosconfig)
		if err != nil || cfg.Contexts[cfg.Context] == nil {
			return r, errors.New("cannot verify retained cluster identity")
		}
		ca, err := base64.StdEncoding.DecodeString(cfg.Contexts[cfg.Context].CA)
		if err != nil {
			return r, errors.New("cannot verify retained cluster identity")
		}
		block, _ := pem.Decode(ca)
		if block == nil {
			return r, errors.New("cannot verify retained cluster identity")
		}
		hash := sha256.Sum256(block.Bytes)
		identity := hex.EncodeToString(hash[:])
		registered, err := registry.List(ctx)
		if err != nil {
			return r, errors.New("cannot verify cluster registry before cleanup")
		}
		for _, cluster := range registered {
			if cluster.Identity == identity {
				return r, errors.New("cluster is already registered; failed-resource cleanup refused")
			}
		}
	}
	return r, nil
}
func (s *ProvisionService) cleanupMachine(ctx context.Context, e *jobs.Execution, plan *provisionState, provider proxmox.MachineProvider) error {
	r, err := s.cleanupRecord(ctx, plan.Spec)
	if err != nil {
		return err
	}
	if err := provider.VerifyOwned(ctx, r.Machine()); err != nil {
		return err
	}
	if err := e.Checkpoint(ctx, "cleanup-machine", "Deleting only the verified machine retained by failed provisioning"); err != nil {
		return err
	}
	if err := beginMachineIntent(ctx, e, "delete", r); err != nil {
		return err
	}
	if err := provider.DeleteOwned(ctx, r.Machine()); err != nil {
		return fmt.Errorf("%w: failed-resource deletion not confirmed", jobs.ErrUncertain)
	}
	// A timeout or provider error above leaves the intent UNKNOWN. No retry.
	if err := proveMachineIntent(ctx, e, "delete", r); err != nil {
		return err
	}
	r.Status = "deleted"
	return saveOwned(ctx, s.Store, r, false)
}

func providerPreflight(ctx context.Context, p proxmox.MachineProvider, machines []proxmox.MachineSpec) error {
	if checker, ok := p.(interface {
		PreflightMachines(context.Context, []proxmox.MachineSpec) error
	}); ok {
		return checker.PreflightMachines(ctx, machines)
	}
	return nil
}

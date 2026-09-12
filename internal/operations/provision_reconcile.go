package operations

import (
	"context"
	"errors"
	"strings"
	"time"

	"talosdeck/internal/jobs"
	"talosdeck/internal/reconcile"
)

// ReconcileJob observes provider ownership only. It cannot resume, acknowledge,
// create or delete resources. Missing provider data is never proof of absence.
func (s *ProvisionService) ReconcileJob(ctx context.Context, m *jobs.Manager, id string) (jobs.Job, error) {
	j, err := m.Get(id)
	if err != nil {
		return j, err
	}
	if j.ClusterID != s.ClusterID || j.Request.ProvisionID == "" {
		return j, errors.New("job does not belong to this provisioning scope")
	}
	if j.Status == "running" || j.Status == "queued" {
		return j, errors.New("active job cannot be reconciled")
	}
	if len(j.Intents) == 0 {
		return j, errors.New("job has no durable provider intents")
	}
	plan, err := s.load(ctx, j.Request.ProvisionID)
	if err != nil || plan.ClusterID != s.ClusterID {
		return j, errors.New("provisioning plan unavailable in this scope")
	}
	for _, intent := range j.Intents {
		if err := ctx.Err(); err != nil {
			return j, err
		}
		machineID := strings.TrimPrefix(intent.ID, intent.Action+":")
		belongs := plan.Spec.MachineID == machineID
		for _, candidate := range plan.MachineIDs {
			belongs = belongs || candidate == machineID
		}
		if !belongs || machineID == intent.ID {
			return j, errors.New("intent ownership is not bound to this plan")
		}
		record, err := loadOwned(ctx, s.Store, machineID)
		if err != nil || record.ClusterID != s.ClusterID || machineIntentIdentity(record) != intent.Identity {
			return j, errors.New("intent ownership identity cannot be verified")
		}
		// Successful persisted delete-task evidence remains evidence. We have no
		// authoritative provider task ID for ambiguous deletes, so no GET error or
		// permission-filtered inventory is allowed to convert them into success.
		if intent.Action == "delete" && intent.Outcome == "succeeded" && intent.Evidence != nil && intent.Evidence.Source == "provider_delete_task_completed" {
			continue
		}
		observed := reconcile.Observation{State: "unknown", Identity: intent.Identity, ObservedAt: time.Now().UTC()}
		if intent.Action == "create" && intent.Compatible() {
			cfg, err := loadProvider(ctx, s.Store, record.ProviderID)
			if err == nil {
				p, err := s.provider(cfg)
				if err == nil && p.VerifyOwned(ctx, record.Machine()) == nil {
					observed.State = "exists"
					observed.Source = "provider_ownership_verified"
					observed.ObservedAt = time.Now().UTC()
				}
			}
		}
		if _, err = m.ObserveIntent(ctx, id, intent.ID, observed); err != nil {
			return j, errors.New("cannot persist reconciliation evidence")
		}
	}
	return m.Get(id)
}

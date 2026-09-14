package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"talosdeck/internal/reconcile"
)

// ManualContext instruments legacy synchronous routes under their existing
// ReserveManual lock. No journal is created for validation/read-only requests.
func (m *Manager) ManualContext(ctx context.Context, user string) (context.Context, func(error) error) {
	var execution *Execution
	recorder := func(ctx context.Context, action, target string, call func() error) error {
		if execution == nil {
			m.mu.Lock()
			if !m.manual || m.storageErr != nil || len(m.jobs) >= MaxJobs {
				m.mu.Unlock()
				return errors.New("manual mutation journal unavailable")
			}
			now := time.Now().UTC()
			j := &Job{ID: uuid.NewString(), ClusterID: m.clusterID, Request: Request{Kind: "manual-command"}, User: user, Status: "running", WorkflowVersion: 1, PlanVersion: 1, StepSchemaVersion: 1, CreatedAt: now, UpdatedAt: now}
			if err := m.save(j); err != nil {
				m.storageErr = err
				m.mu.Unlock()
				return err
			}
			m.jobs[j.ID] = j
			execution = &Execution{manager: m, id: j.ID}
			m.mu.Unlock()
		}
		return execution.RecordMutation(ctx, action, target, call)
	}
	finish := func(runErr error) error {
		if execution == nil {
			return runErr
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		j := m.jobs[execution.id]
		if ctx.Err() != nil {
			runErr = ErrUncertain
		}
		j.Status = "succeeded"
		for _, intent := range j.Intents {
			if intent.Outcome != "succeeded" && !reconcile.CommandReceipt(intent) {
				runErr = ErrUncertain
			}
		}
		if runErr != nil || ctx.Err() != nil {
			j.Status = "interrupted"
			j.ReconciliationOutcome = reconcile.Unknown
			j.Error = "Manual command outcome requires review; no automatic retry"
		}
		j.UpdatedAt = time.Now().UTC()
		j.Events = appendEvent(j.Events, Event{Time: j.UpdatedAt, Step: j.Status, Message: "Synchronous command journal; acknowledgements are not infrastructure health proofs"})
		if err := m.save(j); err != nil {
			m.storageErr = err
			return err
		}
		return runErr
	}
	return reconcile.WithMutationRecorder(reconcile.WithMutationGuard(ctx, m.validateAuthority), recorder), finish
}

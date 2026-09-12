package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"talosdeck/internal/reconcile"
)

// SetExecutionAuthority is only allowed before execution begins. The supplied
// authority must independently fence superseded instances. Nil is rejected.
func (m *Manager) SetExecutionAuthority(a reconcile.Authority, instance string, epoch uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authorityMu.Lock()
	defer m.authorityMu.Unlock()
	if a == nil || instance == "" || epoch == 0 || m.active != "" || m.manual || m.authority != nil {
		return errors.New("invalid or already active execution authority")
	}
	m.authority = a
	m.instanceID = instance
	m.executorEpoch = epoch
	return nil
}
func (m *Manager) validateAuthority(ctx context.Context) error {
	m.authorityMu.RLock()
	defer m.authorityMu.RUnlock()
	// Configuration is immutable once installed, and installed before submission.
	if m.authority == nil {
		return nil
	} // Legacy callers; not distributed fencing.
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := m.authority.Validate(check, m.instanceID, m.executorEpoch); err != nil {
		return reconcile.ErrAuthority
	}
	return nil
}

// BeginIntent must precede a side effect. An existing intent can never authorize
// a replay, even if its observed result was successful. Observe it instead.
func (e *Execution) BeginIntent(ctx context.Context, id, action string, identity reconcile.Identity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := e.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[e.id]
	if j.StopRequested {
		return ErrStopped
	}
	if m.storageErr != nil {
		return m.storageErr
	}
	if !identity.Valid() || len(id) == 0 || len(id) > 128 || (action != "create" && action != "delete") {
		return errors.New("invalid durable intent")
	}
	if len(j.Intents) >= 256 {
		return errors.New("intent limit reached")
	}
	for _, i := range j.Intents {
		if i.ID == id {
			return ErrUncertain
		}
	}
	if j.WorkflowVersion != 1 || j.PlanVersion != 1 || j.StepSchemaVersion != 1 {
		j.ReconciliationOutcome = reconcile.RequiresReview
		if err := m.save(j); err != nil {
			m.storageErr = err
			return err
		}
		return ErrUncertain
	}
	if err := m.validateAuthority(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	j.Intents = append(j.Intents, reconcile.Intent{ID: id, Action: action, Identity: identity, WorkflowVersion: 1, PlanVersion: 1, StepSchemaVersion: 1, CreatedAt: time.Now().UTC(), ExecutorEpoch: m.executorEpoch, Outcome: reconcile.Unknown})
	j.ReconciliationOutcome = reconcile.Unknown
	if err := m.save(j); err != nil {
		m.storageErr = err
		return err
	}
	if err := m.validateAuthority(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	return nil
}

// ObserveIntent only persists evidence and a conclusion; it never runs a command
// or clears the interrupted-job review gate. Evidence contains identities only,
// never raw provider responses, credentials or arbitrary logs.
func (m *Manager) ObserveIntent(ctx context.Context, jobID, intentID string, o reconcile.Observation) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if o.Source != "" && o.Source != "provider_ownership_verified" && o.Source != "provider_delete_task_completed" {
		return "", errors.New("observation source exceeds limit")
	}
	if !o.Identity.Valid() || (o.State != "exists" && o.State != "absent" && o.State != "unknown") {
		return "", errors.New("invalid bounded observation")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storageErr != nil {
		return "", m.storageErr
	}
	j, ok := m.jobs[jobID]
	if !ok {
		return "", ErrNotFound
	}
	for n := range j.Intents {
		i := &j.Intents[n]
		if i.ID != intentID {
			continue
		}
		i.Outcome = reconcile.Evaluate(*i, o, time.Now().UTC())
		i.Evidence = &o
		j.ReconciliationOutcome = i.Outcome
		for _, other := range j.Intents {
			if other.Outcome == reconcile.Unknown || other.Outcome == reconcile.RequiresReview {
				j.ReconciliationOutcome = reconcile.RequiresReview
				break
			}
		}
		if err := m.save(j); err != nil {
			m.storageErr = err
			return "", err
		}
		return i.Outcome, nil
	}
	return "", ErrNotFound
}

// ObserveIntent attaches a bounded observation to this execution's intent.
func (e *Execution) ObserveIntent(ctx context.Context, intentID string, o reconcile.Observation) (string, error) {
	return e.manager.ObserveIntent(ctx, e.id, intentID, o)
}

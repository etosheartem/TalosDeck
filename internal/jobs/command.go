package jobs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"time"

	"talosdeck/internal/reconcile"
)

var commandAction = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// RecordMutation persists a command intent before dispatch. A receipt proves
// only that the command returned successfully, not that infrastructure converged.
// Existing workflow health checks remain responsible for the latter. On restart
// opaque commands require operator review; they are never replayed by reconcile.
func (e *Execution) RecordMutation(ctx context.Context, action, target string, call func() error) error {
	if !commandAction.MatchString(action) || target == "" {
		return fmt.Errorf("invalid mutation identity")
	}
	e.commandMu.Lock()
	defer e.commandMu.Unlock()
	m := e.manager
	m.mu.Lock()
	j := m.jobs[e.id]
	if j == nil {
		m.mu.Unlock()
		return ErrNotFound
	}
	for _, intent := range j.Intents {
		if intent.Action == "command" && !reconcile.CommandReceipt(intent) {
			m.mu.Unlock()
			return ErrUncertain
		}
	}
	sequence := len(j.Intents)
	owner := j.ClusterID
	if owner == "" {
		owner = "platform"
	}
	instance := m.instanceID
	m.mu.Unlock()
	identity := reconcile.Identity{ProviderID: action, ResourceID: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(target))), Generation: e.id, OwnerID: owner}
	id := fmt.Sprintf("command:%d", sequence)
	if err := e.BeginIntent(ctx, id, "command", identity); err != nil {
		return err
	}
	// A stop/authority failure in the durable-write window blocks the call.
	if err := reconcile.CheckMutation(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	if err := m.validateAuthority(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	callErr := call()
	var rejected *reconcile.RejectedError
	if callErr != nil && !errors.As(callErr, &rejected) {
		return fmt.Errorf("%w: mutation outcome not proved (%s)", ErrUncertain, action)
	}
	outcome, source, state := "succeeded", "command_acknowledged", "acknowledged"
	if rejected != nil {
		outcome, source, state = "failed", "command_rejected", "rejected"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storageErr != nil {
		return m.storageErr
	}
	j = m.jobs[e.id]
	for n := range j.Intents {
		i := &j.Intents[n]
		if i.ID != id {
			continue
		}
		i.Outcome = outcome
		i.Evidence = &reconcile.Observation{Source: source, State: state, Identity: identity, ObservedAt: time.Now().UTC()}
		// Instance is stored separately from resource identity and never derived from target.
		i.ManagementInstanceID = instance
	}
	j.ReconciliationOutcome = "succeeded"
	for _, i := range j.Intents {
		if i.Outcome != "succeeded" && !reconcile.CommandReceipt(i) {
			j.ReconciliationOutcome = reconcile.Unknown
			break
		}
	}
	if err := m.save(j); err != nil {
		m.storageErr = err
		return err
	}
	if rejected != nil {
		return rejected.Err
	}
	return nil
}

// ReviewCommands cannot infer infrastructure success from a transport receipt.
// It preserves receipts, marks unfinished opaque commands for explicit review,
// and never dispatches the recorded command or clears its admission gate.
func (m *Manager) ReviewCommands(ctx context.Context, id string) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	if j.Status == "running" || j.Status == "queued" {
		return Job{}, errors.New("active job cannot be reconciled")
	}
	for _, i := range j.Intents {
		if i.Action != "command" {
			return Job{}, errors.New("resource observations require a typed reconciler")
		}
	}
	j.ReconciliationOutcome = reconcile.RequiresReview
	if err := m.save(j); err != nil {
		m.storageErr = err
		return Job{}, err
	}
	return clone(j), nil
}

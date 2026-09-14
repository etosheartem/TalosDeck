package reconcile

import (
	"context"
	"errors"
)

type mutationRecorderKey struct{}
type MutationRecorder func(context.Context, string, string, func() error) error

func WithMutationRecorder(ctx context.Context, recorder MutationRecorder) context.Context {
	return context.WithValue(ctx, mutationRecorderKey{}, recorder)
}

// Mutate records an individual transport command when called by a job. Target
// must identify the destination, never contain a body, credential or key.
func Mutate(ctx context.Context, action, target string, call func() error) error {
	if recorder, ok := ctx.Value(mutationRecorderKey{}).(MutationRecorder); ok && recorder != nil {
		return recorder(ctx, action, target, call)
	}
	if err := CheckMutation(ctx); err != nil {
		return err
	}
	err := call()
	var rejected *RejectedError
	if errors.As(err, &rejected) {
		return rejected.Err
	}
	return err
}

// RejectedError is only for a conclusive API rejection before any effect (e.g.
// a Kubernetes eviction denied by a PDB), never a timeout or generic server error.
type RejectedError struct{ Err error }

func (e *RejectedError) Error() string { return e.Err.Error() }
func (e *RejectedError) Unwrap() error { return e.Err }
func Rejected(err error) error {
	if err == nil {
		return nil
	}
	return &RejectedError{Err: err}
}

func CommandReceipt(i Intent) bool {
	return i.Action == "command" && i.Evidence != nil && (i.Evidence.Source == "command_acknowledged" && i.Evidence.State == "acknowledged" && i.Outcome == "succeeded" || i.Evidence.Source == "command_rejected" && i.Evidence.State == "rejected" && i.Outcome == "failed")
}

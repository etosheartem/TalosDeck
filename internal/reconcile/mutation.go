package reconcile

import "context"

type mutationGuardKey struct{}

// WithMutationGuard binds nested provider/client mutations to the current
// executor. A successful check never authorizes replay of an earlier intent.
func WithMutationGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, mutationGuardKey{}, guard)
}

func CheckMutation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard, ok := ctx.Value(mutationGuardKey{}).(func(context.Context) error); ok && guard != nil {
		return guard(ctx)
	}
	return nil // Compatibility for non-workflow clients.
}

// CheckRequiredMutation prevents a safety-critical helper being used without
// its workflow's execution admission check.
func CheckRequiredMutation(ctx context.Context) error {
	if guard, ok := ctx.Value(mutationGuardKey{}).(func(context.Context) error); !ok || guard == nil {
		return ErrAuthority
	}
	return CheckMutation(ctx)
}

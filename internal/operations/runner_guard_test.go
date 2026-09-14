package operations

import (
	"context"
	"errors"
	"talosdeck/internal/reconcile"
	"testing"
)

func TestCLIAdmissionPreventsProcessLaunch(t *testing.T) {
	ctx := reconcile.WithMutationGuard(context.Background(), func(context.Context) error { return reconcile.ErrAuthority })
	// A nonexistent executable distinguishes refused admission from any launch attempt.
	err := (CLI{Path: "/nonexistent/td31-mutation"}).Run(ctx, []string{"reboot"}, func(string) error { return nil })
	if !errors.Is(err, reconcile.ErrAuthority) {
		t.Fatalf("expected admission refusal, got %v", err)
	}
	err = (CLI{Path: "/nonexistent/td31-mutation"}).Run(ctx, []string{"upgrade-k8s", "--dry-run"}, func(string) error { return nil })
	if err == nil || errors.Is(err, reconcile.ErrAuthority) {
		t.Fatalf("dry run should reach executable lookup: %v", err)
	}
}

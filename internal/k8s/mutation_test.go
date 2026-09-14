package k8s

import (
	"context"
	"errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"talosdeck/internal/reconcile"
	"testing"
)

func TestMutationResultOnlyClassifiesConclusiveRejections(t *testing.T) {
	for _, tc := range []struct {
		err      error
		rejected bool
	}{
		{apierrors.NewTooManyRequests("PDB", 1), true},
		{apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, "worker", errors.New("version")), true},
		{apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "worker", errors.New("denied")), true},
		{apierrors.NewUnauthorized("expired"), true},
		{apierrors.NewInternalError(errors.New("unknown")), false},
		{context.DeadlineExceeded, false},
		{errors.New("connection lost"), false},
	} {
		got := mutationResult(tc.err)
		var rejected *reconcile.RejectedError
		if errors.As(got, &rejected) != tc.rejected {
			t.Fatalf("%v classified incorrectly", tc.err)
		}
		if !errors.Is(got, tc.err) {
			t.Fatal("original API classification lost")
		}
	}
}

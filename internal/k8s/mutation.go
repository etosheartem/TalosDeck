package k8s

import (
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"talosdeck/internal/reconcile"
)

// These API responses conclusively reject this individual command. Network
// failures, deadlines and server errors still require observation and review.
func mutationResult(err error) error {
	if apierrors.IsTooManyRequests(err) || apierrors.IsConflict(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return reconcile.Rejected(err)
	}
	return err
}

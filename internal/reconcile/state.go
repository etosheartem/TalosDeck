// Package reconcile records observations without replaying infrastructure commands.
package reconcile

import (
	"context"
	"errors"
	"time"
)

const Version = 1
const Unknown = "UNKNOWN"
const RequiresReview = "requires_review"

// Authority must live outside the restored management data. An in-process lock
// or an epoch copied from the backup is not an execution authority.
type Authority interface {
	Validate(context.Context, string, uint64) error
}

var ErrAuthority = errors.New("execution authority unavailable or superseded")

type Identity struct {
	ProviderID string `json:"providerId"`
	ResourceID string `json:"resourceId"`
	Generation string `json:"generation"`
	OwnerID    string `json:"ownerId"`
}
type Observation struct {
	Source     string    `json:"source,omitempty"`
	State      string    `json:"state"` // exists, absent, unknown
	Identity   Identity  `json:"identity"`
	ObservedAt time.Time `json:"observedAt"`
}
type Intent struct {
	ManagementInstanceID string       `json:"managementInstanceId,omitempty"`
	ID                   string       `json:"id"`
	Action               string       `json:"action"`
	WorkflowVersion      int          `json:"workflowVersion"`
	PlanVersion          int          `json:"planVersion"`
	StepSchemaVersion    int          `json:"stepSchemaVersion"`
	Identity             Identity     `json:"identity"`
	CreatedAt            time.Time    `json:"createdAt"`
	ExecutorEpoch        uint64       `json:"executorEpoch"`
	Outcome              string       `json:"outcome"`
	Evidence             *Observation `json:"evidence,omitempty"`
}

func (i Intent) Compatible() bool {
	return i.WorkflowVersion == Version && i.PlanVersion == Version && i.StepSchemaVersion == Version
}
func (i Identity) Valid() bool {
	return len(i.ProviderID) > 0 && len(i.ProviderID) <= 256 && len(i.ResourceID) > 0 && len(i.ResourceID) <= 256 && len(i.Generation) > 0 && len(i.Generation) <= 256 && len(i.OwnerID) > 0 && len(i.OwnerID) <= 256
}

// Evaluate never interprets a request error as proof of failure. Absence must be
// an authoritative observation of the exact durable identity, not a name search.
func Evaluate(i Intent, o Observation, now time.Time) string {
	if !i.Compatible() || !i.Identity.Valid() {
		return RequiresReview
	}
	if o.State == "unknown" || o.ObservedAt.IsZero() || o.ObservedAt.Before(i.CreatedAt) || o.ObservedAt.After(now.Add(time.Second)) || now.Sub(o.ObservedAt) > time.Minute {
		return Unknown
	}
	if o.Identity != i.Identity {
		return RequiresReview
	}
	switch i.Action {
	case "create":
		if o.State == "exists" {
			return "succeeded"
		}
		if o.State == "absent" {
			return RequiresReview
		}
	case "delete":
		if o.State == "absent" {
			return "succeeded"
		}
		if o.State == "exists" {
			return RequiresReview
		}
	}
	return RequiresReview
}

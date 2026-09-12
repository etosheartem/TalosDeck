package reconcile

import (
	"testing"
	"time"
)

func TestUnknownAndIncompatibleNeverProveSuccess(t *testing.T) {
	now := time.Now().UTC()
	id := Identity{"p", "r", "g", "owner"}
	i := Intent{Action: "delete", WorkflowVersion: 1, PlanVersion: 1, StepSchemaVersion: 1, Identity: id, CreatedAt: now.Add(-time.Minute)}
	o := Observation{State: "absent", Identity: id, ObservedAt: now}
	if Evaluate(i, o, now) != "succeeded" {
		t.Fatal("exact observed absence")
	}
	o.State = "unknown"
	if Evaluate(i, o, now) != Unknown {
		t.Fatal("outage mistaken for absence")
	}
	o.State = "absent"
	o.Identity.Generation = "reused-resource-id"
	if Evaluate(i, o, now) != RequiresReview {
		t.Fatal("reused identity adopted")
	}
	o.Identity = id
	i.StepSchemaVersion = 99
	if Evaluate(i, o, now) != RequiresReview {
		t.Fatal("unknown schema accepted")
	}
}

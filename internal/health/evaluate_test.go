package health

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func healthyFixture(now time.Time) Snapshot {
	s := Snapshot{ID: "snapshot-1", ClusterID: "cluster-1", CheckedAt: now}
	for _, p := range policies {
		s.Checks = append(s.Checks, Check{ID: p.id + "-check", Category: p.id, State: "healthy", ObservedAt: now})
	}
	return s
}
func TestEmptyMissingAndStaleHaveNoCurrentScore(t *testing.T) {
	now := time.Now().UTC()
	for _, s := range []Snapshot{{}, {ID: "empty", CheckedAt: now}} {
		r := Evaluate(s, now)
		if r.Score != nil || r.ObservedScore != nil || r.Coverage != 0 || r.Status != "unknown" {
			t.Fatal("empty claimed health", r)
		}
	}
	s := healthyFixture(now)
	s.Checks = s.Checks[:7]
	r := Evaluate(s, now)
	if r.Score != nil || r.Coverage != 95 || r.ObservedScore == nil || *r.ObservedScore != 100 {
		t.Fatal("missing category hidden", r)
	}
	s = healthyFixture(now.Add(-11 * time.Minute))
	r = Evaluate(s, now)
	if !r.Stale || r.Score != nil || r.ObservedScore != nil || r.Coverage != 0 {
		t.Fatal("stale current score")
	}
}
func TestPartialCoverageAndCriticalDominance(t *testing.T) {
	now := time.Now().UTC()
	s := healthyFixture(now)
	s.Checks = append(s.Checks, Check{ID: "unknown-node", Category: "nodes", State: "unknown", ObservedAt: now})
	s.Checks[1].State = "critical"
	s.Checks[1].Reason = "quorum-lost"
	r := Evaluate(s, now)
	if r.Score != nil || r.Status != "critical" || r.Coverage != 92.5 {
		t.Fatal(r)
	}
	if r.Categories[1].Score == nil || *r.Categories[1].Score != 0 {
		t.Fatal("quorum penalty wrong")
	}
	if r.Categories[2].Score != nil || r.Categories[2].Coverage != 50 {
		t.Fatal("partial node coverage wrong")
	}
}
func TestDeterministicDedupAndNoMutation(t *testing.T) {
	now := time.Now().UTC()
	s := healthyFixture(now)
	s.Checks[0].State = "warning"
	s.Checks = append(s.Checks, s.Checks[0], s.Checks[0])
	before, _ := json.Marshal(s)
	r := Evaluate(s, now)
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("input mutated")
	}
	for i, j := 0, len(s.Checks)-1; i < j; i, j = i+1, j-1 {
		s.Checks[i], s.Checks[j] = s.Checks[j], s.Checks[i]
	}
	second := Evaluate(s, now)
	if !reflect.DeepEqual(r, second) {
		t.Fatal("order affected result")
	}
	if len(r.Categories[0].Checks) != 1 || len(r.Categories[0].Deductions) != 1 || *r.Score != 96.25 {
		t.Fatal("duplicate penalty")
	}
	if r.SnapshotID != "snapshot-1" {
		t.Fatal("snapshot identity lost")
	}
}
func TestCertificateStateComesFromCollectorAndExplicitNA(t *testing.T) {
	now := time.Now().UTC()
	s := healthyFixture(now)
	s.Checks[7].Reason = "auto-rotating-short-lived"
	r := Evaluate(s, now)
	if *r.Score != 100 {
		t.Fatal("healthy auto-rotating cert penalized")
	}
	s.Checks[7].State = "critical"
	s.Checks[7].Reason = "certificate-expired"
	r = Evaluate(s, now)
	if *r.Score != 95 || *r.Categories[7].Score != 0 {
		t.Fatal("expiry penalty")
	}
	s.Checks[7].State = "not-applicable"
	r = Evaluate(s, now)
	if *r.Score != 100 || r.Coverage != 100 {
		t.Fatal("explicit NA mishandled")
	}
	s.Checks[7].ObservedAt = now.Add(-11 * time.Minute)
	r = Evaluate(s, now)
	if r.Score != nil {
		t.Fatal("stale NA treated authoritative")
	}
}

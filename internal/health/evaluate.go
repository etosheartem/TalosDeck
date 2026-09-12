// Package health evaluates an immutable observation snapshot without network or storage access.
package health

import (
	"encoding/json"
	"sort"
	"time"
)

const Freshness = 10 * time.Minute

type Check struct {
	ID              string    `json:"id"`
	Category        string    `json:"category"`
	Resource        string    `json:"resource,omitempty"`
	Node            string    `json:"node,omitempty"`
	State           string    `json:"state"`
	Reason          string    `json:"reason"`
	Title           string    `json:"title"`
	Details         string    `json:"details"`
	SuggestedAction string    `json:"suggestedAction,omitempty"`
	ObservedAt      time.Time `json:"observedAt"`
}
type Snapshot struct {
	ID        string    `json:"id"`
	ClusterID string    `json:"clusterId"`
	CheckedAt time.Time `json:"checkedAt"`
	Checks    []Check   `json:"checks"`
}
type Deduction struct {
	CheckID string  `json:"checkId"`
	Points  float64 `json:"points"`
	Reason  string  `json:"reason"`
}
type Category struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Weight        float64     `json:"weight"`
	Score         *float64    `json:"score"`
	ObservedScore *float64    `json:"observedScore"`
	Coverage      float64     `json:"coverage"`
	Status        string      `json:"status"`
	Deductions    []Deduction `json:"deductions"`
	Checks        []Check     `json:"checks"`
}
type Report struct {
	PolicyVersion int        `json:"policyVersion"`
	SnapshotID    string     `json:"snapshotId"`
	CheckedAt     time.Time  `json:"checkedAt"`
	Stale         bool       `json:"stale"`
	Status        string     `json:"status"`
	Score         *float64   `json:"score"`
	ObservedScore *float64   `json:"observedScore"`
	Coverage      float64    `json:"coverage"`
	Categories    []Category `json:"categories"`
	Findings      []Check    `json:"findings"`
}

var policies = []struct {
	id, name string
	weight   float64
}{{"control-plane", "Control plane", 15}, {"etcd", "Etcd", 20}, {"nodes", "Nodes", 15}, {"storage", "Storage", 15}, {"networking", "Networking", 10}, {"workloads", "Workloads", 10}, {"backups", "Backups", 10}, {"certificates", "Certificates", 5}}

func fresh(at, now time.Time) bool { return !at.IsZero() && !at.After(now) && now.Sub(at) <= Freshness }
func number(v float64) *float64    { return &v }
func rank(state string) int {
	switch state {
	case "critical":
		return 4
	case "warning":
		return 3
	case "unknown":
		return 2
	case "healthy":
		return 1
	case "not-applicable":
		return 0
	}
	return 2
}
func penalty(c Check) float64 {
	if c.State == "critical" {
		if c.Reason == "quorum-lost" || c.Reason == "certificate-expired" || c.Reason == "not-yet-valid" {
			return 100
		}
		return 75
	}
	if c.State == "warning" {
		return 25
	}
	return 0
}

// Evaluate uses the worst known check in each category. Unknown data reduces
// coverage, never implies a successful check, and never receives a current score.
func Evaluate(snapshot Snapshot, now time.Time) Report {
	r := Report{PolicyVersion: 1, SnapshotID: snapshot.ID, CheckedAt: snapshot.CheckedAt, Stale: !fresh(snapshot.CheckedAt, now), Status: "unknown", Categories: []Category{}, Findings: []Check{}}
	snapshotStale := r.Stale
	knownCategories := map[string]bool{}
	for _, p := range policies {
		knownCategories[p.id] = true
	}
	dedup := map[string]Check{}
	for _, original := range snapshot.Checks {
		c := original
		if !knownCategories[c.Category] {
			continue
		}
		if c.ID == "" {
			c.ID = "missing-id:" + c.Category
			c.State = "unknown"
			c.Reason = "missing-check-identity"
		}
		if snapshotStale || !fresh(c.ObservedAt, now) {
			r.Stale = true
			c.State = "unknown"
			c.Reason = "stale-observation"
		}
		switch c.State {
		case "healthy", "warning", "critical", "unknown", "not-applicable":
		default:
			c.State = "unknown"
			c.Reason = "invalid-observation-state"
		}
		key := c.Category + "\x00" + c.ID
		old, exists := dedup[key]
		if exists {
			a, _ := json.Marshal(c)
			b, _ := json.Marshal(old)
			if rank(c.State) < rank(old.State) || (rank(c.State) == rank(old.State) && (penalty(c) < penalty(old) || (penalty(c) == penalty(old) && string(a) >= string(b)))) {
				continue
			}
		}
		dedup[key] = c
	}
	totalCurrent, observedSum, observedWeight := 0.0, 0.0, 0.0
	complete := true
	critical, warning := false, false
	for _, p := range policies {
		cat := Category{ID: p.id, Name: p.name, Weight: p.weight, Status: "unknown", Deductions: []Deduction{}, Checks: []Check{}}
		for _, c := range dedup {
			if c.Category == p.id {
				cat.Checks = append(cat.Checks, c)
			}
		}
		sort.Slice(cat.Checks, func(i, j int) bool { return cat.Checks[i].ID < cat.Checks[j].ID })
		if len(cat.Checks) == 0 {
			cat.Checks = append(cat.Checks, Check{ID: "missing-source:" + p.id, Category: p.id, State: "unknown", Reason: "missing-category", Title: "Observation unavailable"})
		}
		known, unknown := 0, 0
		worst := 0.0
		worstID, worstReason := "", ""
		catCritical, catWarning := false, false
		for _, c := range cat.Checks {
			switch c.State {
			case "not-applicable":
				known++
			case "unknown":
				unknown++
			case "healthy", "warning", "critical":
				known++
				points := penalty(c)
				if points > worst {
					worst = points
					worstID = c.ID
					worstReason = c.Reason
				}
				if c.State == "critical" {
					catCritical = true
					critical = true
				}
				if c.State == "warning" {
					catWarning = true
					warning = true
				}
			}
			if c.State == "critical" || c.State == "warning" || c.State == "unknown" {
				r.Findings = append(r.Findings, c)
			}
		}
		cat.Coverage = 100 * float64(known) / float64(known+unknown)
		r.Coverage += cat.Coverage * p.weight / 100
		if worst > 0 {
			cat.Deductions = append(cat.Deductions, Deduction{CheckID: worstID, Points: worst, Reason: worstReason})
		}
		if known > 0 {
			cat.ObservedScore = number(100 - worst)
			w := p.weight * cat.Coverage / 100
			observedSum += (100 - worst) * w
			observedWeight += w
		}
		if unknown == 0 {
			cat.Score = number(100 - worst)
			totalCurrent += (100 - worst) * p.weight / 100
		} else {
			complete = false
		}
		switch {
		case catCritical:
			cat.Status = "critical"
		case catWarning:
			cat.Status = "warning"
		case unknown > 0:
			cat.Status = "unknown"
		default:
			cat.Status = "healthy"
		}
		r.Categories = append(r.Categories, cat)
	}
	if observedWeight > 0 {
		r.ObservedScore = number(observedSum / observedWeight)
	}
	if complete && !r.Stale {
		r.Score = number(totalCurrent)
	}
	switch {
	case critical:
		r.Status = "critical"
	case warning:
		r.Status = "warning"
	case !complete || r.Stale:
		r.Status = "unknown"
	default:
		r.Status = "healthy"
	}
	sort.Slice(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Category == b.Category {
			return a.ID < b.ID
		}
		return a.Category < b.Category
	})
	return r
}

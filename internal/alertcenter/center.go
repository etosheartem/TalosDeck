package alertcenter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"talosdeck/internal/clusters"
)

var ErrConflict = errors.New("alert settings changed; reload before saving")
var ErrNotFound = errors.New("alert center resource not found")

const maxActive = 5000
const maxDeliveries = 2000

type storedChannel struct {
	Channel
	Config map[string]string `json:"config"`
}
type storedDelivery struct {
	Delivery
	Payload Notification `json:"payload"`
	Slot    int64        `json:"slot"`
	IsTest  bool         `json:"isTest"`
}
type dispatchMark struct {
	AlertID      string    `json:"alertId"`
	Transition   int       `json:"transition"`
	LastQueuedAt time.Time `json:"lastQueuedAt"`
}
type state struct {
	Dispatch       map[string]dispatchMark   `json:"dispatch"`
	Version        int                       `json:"version"`
	Alerts         map[string]Alert          `json:"alerts"`
	Channels       map[string]storedChannel  `json:"channels"`
	Deliveries     map[string]storedDelivery `json:"deliveries"`
	Silences       map[string]Silence        `json:"silences"`
	Routes         RoutesConfig              `json:"routes"`
	LastCheckAt    time.Time                 `json:"lastCheckAt"`
	LegacyMigrated bool                      `json:"legacyMigrated"`
}
type Center struct {
	lastPersisted []byte
	inFlight      map[string]bool
	mu            sync.Mutex
	store         Store
	clusterID     string
	sender        Sender
	state         state
	health        error
	now           func() time.Time
	running       bool
}

func emptyState() state {
	return state{Version: 1, Dispatch: map[string]dispatchMark{}, Alerts: map[string]Alert{}, Channels: map[string]storedChannel{}, Deliveries: map[string]storedDelivery{}, Silences: map[string]Silence{}, Routes: RoutesConfig{Routes: []Route{}}}
}
func Open(ctx context.Context, store Store, clusterID string, sender Sender) (*Center, error) {
	if store == nil || clusterID == "" {
		return nil, errors.New("alert center storage and scope required")
	}
	c := &Center{inFlight: map[string]bool{}, store: store, clusterID: strings.Clone(clusterID), sender: sender, state: emptyState(), now: func() time.Time { return time.Now().UTC() }}
	raw, err := store.GetSecret(ctx, clusterID, "alert-center", "state")
	if err != nil && !errors.Is(err, clusters.ErrNotFound) {
		return nil, errors.New("alert center state unavailable")
	}
	if err == nil {
		if len(raw) > 16<<20 || json.Unmarshal(raw, &c.state) != nil || c.state.Version != 1 || c.state.Alerts == nil || c.state.Channels == nil || c.state.Deliveries == nil || c.state.Silences == nil || c.state.Dispatch == nil {
			return nil, errors.New("invalid alert center state")
		}
		for id, a := range c.state.Alerts {
			if a.State == "active" {
				a.Observation = "stale"
				c.state.Alerts[id] = a
			}
		}
		for id, d := range c.state.Deliveries {
			if d.State == "sending" {
				d.State = "uncertain"
				d.ErrorCode = "restart-during-delivery"
				c.state.Deliveries[id] = d
			}
		}
	}
	if err = c.persist(ctx, c.state); err != nil {
		return nil, err
	}
	return c, nil
}
func cloneState(in state) state {
	raw, _ := json.Marshal(in)
	var out state
	_ = json.Unmarshal(raw, &out)
	return out
}
func (c *Center) persist(ctx context.Context, next state) error {
	trimHistory(&next)
	active := 0
	for _, a := range next.Alerts {
		if a.State == "active" {
			active++
		}
	}
	if active > maxActive || len(next.Deliveries) > maxDeliveries {
		c.health = errors.Join(ErrUnavailable, errors.New("alert center capacity exceeded"))
		return c.health
	}
	raw, err := json.Marshal(next)
	if err == nil && len(raw) > 16<<20 {
		err = errors.New("alert center state too large")
	}
	if err == nil && c.health == nil && bytes.Equal(raw, c.lastPersisted) {
		return nil
	}
	if err == nil {
		err = c.store.PutSecret(ctx, c.clusterID, "alert-center", "state", raw)
	}
	if err != nil {
		c.health = errors.Join(ErrUnavailable, errors.New("alert center state could not be persisted"))
		return c.health
	}
	var owned state
	_ = json.Unmarshal(raw, &owned)
	c.state = owned
	c.lastPersisted = append([]byte(nil), raw...)
	c.health = nil
	return nil
}
func trimHistory(s *state) {
	defer func() {
		for key, mark := range s.Dispatch {
			if _, exists := s.Alerts[mark.AlertID]; !exists {
				delete(s.Dispatch, key)
			}
		}
	}()
	resolved := []Alert{}
	for _, a := range s.Alerts {
		if a.State == "resolved" {
			resolved = append(resolved, a)
		}
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].LastChanged.After(resolved[j].LastChanged) })
	if len(resolved) > 1000 {
		for _, a := range resolved[1000:] {
			delete(s.Alerts, a.ID)
		}
	}
	terminal := []storedDelivery{}
	for _, d := range s.Deliveries {
		if d.State == "sent" || d.State == "suppressed" {
			terminal = append(terminal, d)
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].CreatedAt.Before(terminal[j].CreatedAt) })
	for _, d := range terminal {
		if len(s.Deliveries) <= maxDeliveries {
			break
		}
		delete(s.Deliveries, d.ID)
	}
}

var sensitive = regexp.MustCompile(`(?i)(bearer\s|token\s*[=:]|password\s*[=:]|secret\s*[=:]|private[ _-]?key|-----BEGIN|client-key-data)`)

func safeText(s string, n int) string {
	if sensitive.MatchString(s) {
		return "[redacted sensitive details]"
	}
	s = strings.Map(func(r rune) rune {
		if r < ' ' && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}
func validID(s string) bool { return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "\r\n\x00\t") }
func severityScore(s string) int {
	switch s {
	case "info":
		return 1
	case "warning":
		return 2
	case "critical":
		return 3
	}
	return 0
}
func fingerprint(cluster, rule, resource string) string {
	raw, _ := json.Marshal([]string{cluster, rule, resource})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func current(s state, fp string) (Alert, bool) {
	var found Alert
	ok := false
	for _, a := range s.Alerts {
		if a.Fingerprint == fp && (!ok || a.FirstSeen.After(found.FirstSeen)) {
			found = a
			ok = true
		}
	}
	return found, ok
}
func (c *Center) Observe(ctx context.Context, observations []Observation) error {
	if len(observations) > 10000 {
		return invalid("too many alert observations")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	next := cloneState(c.state)
	now := c.now()
	for _, o := range observations {
		if !validID(o.RuleID) || !validID(o.ResourceID) || severityScore(o.Severity) == 0 {
			return invalid("invalid alert observation")
		}
		fp := fingerprint(c.clusterID, o.RuleID, o.ResourceID)
		a, exists := current(next, fp)
		if !o.Known {
			if exists && a.State == "active" {
				a.Observation = "stale"
				next.Alerts[a.ID] = a
			}
			continue
		}
		if o.Event {
			if exists {
				continue
			}
			a = Alert{ID: uuid.NewString(), ClusterID: c.clusterID, Fingerprint: fp, RuleID: o.RuleID, ResourceID: o.ResourceID, Node: safeText(o.Node, 256), Component: safeText(o.Component, 128), Severity: o.Severity, State: "resolved", Title: safeText(o.Title, 256), Details: safeText(o.Details, 2048), SuggestedAction: safeText(o.SuggestedAction, 1024), FirstSeen: now, LastSeen: now, LastChanged: now, ResolvedAt: &now, Occurrences: 1, Transition: 1, Observation: "known", Event: true}
			next.Alerts[a.ID] = a
			queueAlert(&next, a, now, 0)
			continue
		}
		if !o.Active {
			if exists && a.State == "active" {
				a.State = "resolved"
				a.Observation = "known"
				a.LastSeen = now
				a.LastChanged = now
				a.ResolvedAt = &now
				a.Transition++
				next.Alerts[a.ID] = a
				queueAlert(&next, a, now, 0)
			}
			continue
		}
		changed := !exists || a.State == "resolved" || a.Severity != o.Severity
		if !exists || a.State == "resolved" {
			a = Alert{ID: uuid.NewString(), ClusterID: c.clusterID, Fingerprint: fp, RuleID: o.RuleID, ResourceID: o.ResourceID, FirstSeen: now, Transition: 0}
		}
		a.Node = safeText(o.Node, 256)
		a.Component = safeText(o.Component, 128)
		a.State = "active"
		a.Severity = o.Severity
		a.Title = safeText(o.Title, 256)
		a.Details = safeText(o.Details, 2048)
		a.SuggestedAction = safeText(o.SuggestedAction, 1024)
		a.LastSeen = now
		a.Occurrences++
		a.Observation = "known"
		if changed {
			a.LastChanged = now
			a.Transition++
		}
		next.Alerts[a.ID] = a
		if changed {
			queueAlert(&next, a, now, 0)
		}
	}
	next.LastCheckAt = now
	return c.persist(ctx, next)
}
func (c *Center) MarkStale(ctx context.Context, component string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := cloneState(c.state)
	for id, a := range next.Alerts {
		if a.State == "active" && (component == "" || a.Component == component) {
			a.Observation = "stale"
			next.Alerts[id] = a
		}
	}
	return c.persist(ctx, next)
}
func matches(route Route, a Alert) bool {
	if !route.Enabled || severityScore(a.Severity) < severityScore(route.MinSeverity) || a.State == "resolved" && !a.Event && !route.SendResolved {
		return false
	}
	contains := func(v []string, x string) bool {
		if len(v) == 0 {
			return true
		}
		for _, s := range v {
			if s == x {
				return true
			}
		}
		return false
	}
	return contains(route.RuleIDs, a.RuleID) && contains(route.NodeIDs, a.Node)
}
func silenced(s state, a Alert, now time.Time) *time.Time {
	var end *time.Time
	for _, q := range s.Silences {
		if now.Before(q.StartsAt) || !now.Before(q.EndsAt) || q.RuleID != "" && q.RuleID != a.RuleID || q.Node != "" && q.Node != a.Node || q.Severity != "" && q.Severity != a.Severity {
			continue
		}
		if end == nil || q.EndsAt.After(*end) {
			v := q.EndsAt
			end = &v
		}
	}
	return end
}
func queueAlert(s *state, a Alert, now time.Time, slot int64) {
	queueRoutes(s, a, now, slot, s.Routes.Routes)
}
func queueRoutes(s *state, a Alert, now time.Time, slot int64, routes []Route) {
	for _, route := range routes {
		if !matches(route, a) {
			continue
		}
		if slot != 0 && route.RepeatMinutes == 0 {
			continue
		}
		for _, id := range route.ChannelIDs {
			channel, ok := s.Channels[id]
			if !ok || !channel.Enabled {
				continue
			}
			if slot != 0 {
				blocked := false
				for _, prior := range s.Deliveries {
					if prior.AlertID == a.ID && prior.ChannelID == id && prior.State != "sent" && prior.State != "suppressed" {
						blocked = true
						break
					}
				}
				if blocked {
					continue
				}
			}
			markKey := a.ID + ":" + id
			mark, marked := s.Dispatch[markKey]
			if marked && mark.Transition == a.Transition {
				if slot == 0 || route.RepeatMinutes == 0 || now.Sub(mark.LastQueuedAt) < time.Duration(route.RepeatMinutes)*time.Minute {
					continue
				}
			}
			dedupe := fingerprint(a.ID, id, fmtKey(a.Transition, 0, slot))
			if _, exists := s.Deliveries[dedupe]; exists {
				continue
			}
			next := now
			code := ""
			if end := silenced(*s, a, now); end != nil {
				next = *end
				code = "silenced"
			}
			n := Notification{ID: dedupe, ClusterID: a.ClusterID, AlertID: a.ID, State: a.State, Severity: a.Severity, Title: a.Title, Details: a.Details, Node: a.Node, RuleID: a.RuleID, OccurredAt: a.LastChanged}
			s.Dispatch[markKey] = dispatchMark{AlertID: a.ID, Transition: a.Transition, LastQueuedAt: now}
			s.Deliveries[dedupe] = storedDelivery{Delivery: Delivery{ID: dedupe, AlertID: a.ID, Transition: a.Transition, ChannelID: id, ChannelGeneration: channel.Generation, State: "pending", ErrorCode: code, NextAttemptAt: next, CreatedAt: now}, Payload: n, Slot: slot}
		}
	}
}
func fmtKey(transition, generation int, slot int64) string {
	b, _ := json.Marshal([]any{transition, generation, slot})
	return string(b)
}
func (c *Center) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := Snapshot{Alerts: []Alert{}, Health: "healthy", LastCheckAt: c.state.LastCheckAt}
	if c.health != nil {
		out.Health = "degraded"
	}
	now := c.now()
	for _, a := range cloneState(c.state).Alerts {
		a.SilencedUntil = silenced(c.state, a, now)
		if a.State == "active" {
			out.Summary.Active++
			if a.Severity == "critical" {
				out.Summary.Critical++
			}
			if a.Severity == "warning" {
				out.Summary.Warning++
			}
			if a.SilencedUntil != nil {
				out.Summary.Silenced++
			}
			if a.Observation == "stale" {
				out.Summary.Stale++
			}
		}
		out.Alerts = append(out.Alerts, a)
	}
	sort.Slice(out.Alerts, func(i, j int) bool { return out.Alerts[i].LastChanged.After(out.Alerts[j].LastChanged) })
	return out
}
func (c *Center) Alert(id string) (Alert, bool) {
	for _, a := range c.Snapshot().Alerts {
		if a.ID == id {
			return a, true
		}
	}
	return Alert{}, false
}
func (c *Center) Health() error { c.mu.Lock(); defer c.mu.Unlock(); return c.health }

package alertcenter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrInvalid = errors.New("invalid alert center request")
var ErrUnavailable = errors.New("alert center unavailable")

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }
func (c *Center) Channels() []Channel {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Channel{}
	for _, v := range c.state.Channels {
		out = append(out, v.Channel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (c *Center) SaveChannel(ctx context.Context, in ChannelInput) (Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := cloneState(c.state)
	now := c.now()
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 100 || len(in.RecipientUser) > 100 {
		return Channel{}, invalid("channel name or recipient invalid")
	}
	old, exists := next.Channels[in.ID]
	if in.ID != "" && !exists {
		return Channel{}, ErrNotFound
	}
	if exists && in.Generation != old.Generation {
		return Channel{}, ErrConflict
	}
	if !exists && len(next.Channels) >= 50 {
		return Channel{}, invalid("channel limit reached")
	}
	cfg := in.Config
	if exists && len(cfg) == 0 {
		cfg = old.Config
	}
	if err := ValidateChannelConfig(in.Type, cfg); err != nil {
		return Channel{}, invalid("channel configuration invalid")
	}
	channel := Channel{ID: in.ID, Name: safeText(in.Name, 100), Type: in.Type, RecipientUser: in.RecipientUser, Enabled: in.Enabled, Configured: true, Generation: 1, CreatedAt: now, UpdatedAt: now}
	if exists {
		channel.CreatedAt = old.CreatedAt
		channel.Generation = old.Generation + 1
	} else {
		channel.ID = uuid.NewString()
	}
	copied := map[string]string{}
	for k, v := range cfg {
		copied[strings.Clone(k)] = strings.Clone(v)
	}
	next.Channels[channel.ID] = storedChannel{channel, copied}
	if err := c.persist(ctx, next); err != nil {
		return Channel{}, err
	}
	return channel, nil
}
func (c *Center) DeleteChannel(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := cloneState(c.state)
	if _, ok := next.Channels[id]; !ok {
		return ErrNotFound
	}
	for _, r := range next.Routes.Routes {
		for _, channel := range r.ChannelIDs {
			if channel == id {
				return fmt.Errorf("%w: remove channel from routes first", ErrConflict)
			}
		}
	}
	delete(next.Channels, id)
	for key, d := range next.Deliveries {
		if d.ChannelID == id && (d.State == "pending" || d.State == "retrying") {
			d.State = "suppressed"
			d.ErrorCode = "channel-deleted"
			next.Deliveries[key] = d
		}
	}
	return c.persist(ctx, next)
}
func (c *Center) Routes() RoutesConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneState(c.state).Routes
}
func (c *Center) SaveRoutes(ctx context.Context, in RoutesConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := cloneState(c.state)
	if in.Revision != next.Routes.Revision {
		return ErrConflict
	}
	if len(in.Routes) > 100 {
		return invalid("route limit reached")
	}
	ids := map[string]bool{}
	for i := range in.Routes {
		r := &in.Routes[i]
		if r.ID == "" {
			r.ID = uuid.NewString()
		}
		if !validID(r.ID) || ids[r.ID] || strings.TrimSpace(r.Name) == "" || len(r.Name) > 100 || severityScore(r.MinSeverity) == 0 || len(r.ChannelIDs) == 0 || len(r.ChannelIDs) > 50 || len(r.RuleIDs) > 100 || len(r.NodeIDs) > 100 || r.RepeatMinutes < 0 || r.RepeatMinutes > 10080 || r.RepeatMinutes > 0 && r.RepeatMinutes < 5 {
			return invalid("route fields invalid")
		}
		ids[r.ID] = true
		for _, id := range r.ChannelIDs {
			if _, ok := next.Channels[id]; !ok {
				return invalid("route channel unavailable")
			}
		}
		for _, id := range append(append([]string{}, r.RuleIDs...), r.NodeIDs...) {
			if !validID(id) {
				return invalid("invalid route selector")
			}
		}
		r.Name = safeText(r.Name, 100)
	}
	in.Revision++
	next.Routes = in
	return c.persist(ctx, next)
}
func (c *Center) Silences() []Silence {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Silence{}
	for _, q := range c.state.Silences {
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EndsAt.After(out[j].EndsAt) })
	return out
}
func (c *Center) AddSilence(ctx context.Context, q Silence) (Silence, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if q.StartsAt.IsZero() {
		q.StartsAt = now
	}
	if !q.EndsAt.After(now) || !q.EndsAt.After(q.StartsAt) || q.EndsAt.Sub(now) > 30*24*time.Hour || q.RuleID == "" && q.Node == "" && q.Severity == "" || q.Severity != "" && severityScore(q.Severity) == 0 || strings.TrimSpace(q.Reason) == "" || len(q.Reason) > 500 || q.CreatedBy == "" {
		return Silence{}, invalid("silence requires selector, reason, author and future end within30days")
	}
	if q.RuleID != "" && !validID(q.RuleID) || q.Node != "" && !validID(q.Node) {
		return Silence{}, invalid("invalid silence selector")
	}
	next := cloneState(c.state)
	for id, old := range next.Silences {
		if old.EndsAt.Before(now.Add(-24 * time.Hour)) {
			delete(next.Silences, id)
		}
	}
	if len(next.Silences) >= 1000 {
		return Silence{}, invalid("silence limit reached")
	}
	q.ID = uuid.NewString()
	q.CreatedAt = now
	q.Reason = safeText(q.Reason, 500)
	next.Silences[q.ID] = q
	if err := c.persist(ctx, next); err != nil {
		return Silence{}, err
	}
	return q, nil
}
func (c *Center) DeleteSilence(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := cloneState(c.state)
	if _, ok := next.Silences[id]; !ok {
		return ErrNotFound
	}
	delete(next.Silences, id)
	now := c.now()
	for key, d := range next.Deliveries {
		if d.State == "pending" && d.ErrorCode == "silenced" {
			d.NextAttemptAt = now
			next.Deliveries[key] = d
		}
	}
	return c.persist(ctx, next)
}
func (c *Center) Deliveries() []Delivery {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []Delivery{}
	for _, d := range c.state.Deliveries {
		out = append(out, d.Delivery)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
func (c *Center) RetryDelivery(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := cloneState(c.state)
	d, ok := next.Deliveries[id]
	if !ok {
		return ErrNotFound
	}
	if d.State != "failed" && d.State != "uncertain" {
		return ErrConflict
	}
	d.State = "pending"
	d.Attempts = 0
	d.ErrorCode = ""
	d.NextAttemptAt = c.now()
	next.Deliveries[id] = d
	return c.persist(ctx, next)
}
func (c *Center) TestChannel(ctx context.Context, id, actor string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := cloneState(c.state)
	channel, ok := next.Channels[id]
	if !ok {
		return ErrNotFound
	}
	if !channel.Enabled {
		return invalid("enable channel before testing")
	}
	now := c.now()
	for _, d := range next.Deliveries {
		if d.IsTest && d.ChannelID == id && now.Sub(d.CreatedAt) < time.Minute {
			return fmt.Errorf("%w: wait one minute before another test", ErrConflict)
		}
	}
	key := uuid.NewString()
	n := Notification{ID: key, ClusterID: c.clusterID, State: "test", Severity: "info", Title: "TalosDeck notification test", Details: "An administrator explicitly requested this channel test.", RuleID: "notification.test", OccurredAt: now}
	next.Deliveries[key] = storedDelivery{Delivery: Delivery{ID: key, ChannelID: id, ChannelGeneration: channel.Generation, State: "pending", CreatedAt: now, NextAttemptAt: now}, Payload: n, IsTest: true}
	return c.persist(ctx, next)
}
func (c *Center) ImportLegacyTelegram(ctx context.Context, token, chatID, minLevel string, enabled bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.LegacyMigrated {
		return nil
	}
	next := cloneState(c.state)
	next.LegacyMigrated = true
	if token != "" && chatID != "" {
		cfg := map[string]string{"token": token, "chatId": chatID}
		if err := ValidateChannelConfig("telegram", cfg); err != nil {
			return invalid("legacy Telegram configuration invalid")
		}
		now := c.now()
		id := uuid.NewString()
		next.Channels[id] = storedChannel{Channel: Channel{ID: id, Name: "Migrated Telegram", Type: "telegram", Enabled: enabled, Configured: true, Generation: 1, CreatedAt: now, UpdatedAt: now}, Config: cfg}
		if severityScore(minLevel) == 0 {
			minLevel = "info"
		}
		next.Routes.Routes = append(next.Routes.Routes, Route{ID: uuid.NewString(), Name: "Migrated Telegram", Enabled: enabled, MinSeverity: minLevel, ChannelIDs: []string{id}, SendResolved: true})
		next.Routes.Revision++
	}
	return c.persist(ctx, next)
}

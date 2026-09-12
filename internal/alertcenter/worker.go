package alertcenter

import (
	"context"
	"errors"
	"time"
)

func eligible(s state, d storedDelivery, now time.Time) (bool, string, *time.Time) {
	channel, ok := s.Channels[d.ChannelID]
	if !ok || !channel.Enabled || channel.Generation != d.ChannelGeneration {
		return false, "channel-changed", nil
	}
	if d.IsTest {
		return true, "", nil
	}
	a, ok := s.Alerts[d.AlertID]
	if !ok || a.Transition != d.Transition || a.State != d.Payload.State {
		return false, "alert-changed", nil
	}
	if a.Observation == "stale" {
		next := now.Add(time.Minute)
		return false, "observation-stale", &next
	}
	if end := silenced(s, a, now); end != nil {
		return false, "silenced", end
	}
	for _, route := range s.Routes.Routes {
		if matches(route, a) {
			for _, id := range route.ChannelIDs {
				if id == channel.ID {
					return true, "", nil
				}
			}
		}
	}
	return false, "route-changed", nil
}

// DeliverOnce persists a claim before calling the transport, outside the state
// mutex. Unknown outcomes are not automatically retried: a receiver may have
// accepted the message before the network failed.
func (c *Center) DeliverOnce(ctx context.Context) error {
	c.mu.Lock()
	next := cloneState(c.state)
	now := c.now()
	for id, d := range next.Deliveries {
		if d.State == "sending" && !c.inFlight[id] {
			d.State = "uncertain"
			d.ErrorCode = "delivery-completion-not-persisted"
			next.Deliveries[id] = d
		}
	}
	// Reconcile new routes and optional reminder slots. Existing deterministic
	// delivery IDs prevent restart/poll duplication.
	for _, a := range next.Alerts {
		if a.State != "active" {
			continue
		}
		queueAlert(&next, a, now, 0)
		for _, r := range next.Routes.Routes {
			if r.RepeatMinutes > 0 && matches(r, a) {
				interval := time.Duration(r.RepeatMinutes) * time.Minute
				slot := int64(now.Sub(a.LastChanged) / interval)
				if slot > 0 {
					queueRoutes(&next, a, now, now.UnixNano(), []Route{r})
				}
			}
		}
	}
	var selected *storedDelivery
	for id, d := range next.Deliveries {
		if d.State != "pending" && d.State != "retrying" {
			continue
		}
		if d.NextAttemptAt.After(now) {
			continue
		}
		ok, reason, retry := eligible(next, d, now)
		if !ok {
			d.ErrorCode = reason
			if retry != nil {
				d.NextAttemptAt = *retry
			} else {
				d.State = "suppressed"
			}
			next.Deliveries[id] = d
			continue
		}
		if selected == nil || d.CreatedAt.Before(selected.CreatedAt) {
			copy := d
			selected = &copy
		}
	}
	if selected == nil {
		err := c.persist(ctx, next)
		c.mu.Unlock()
		return err
	}
	if c.sender == nil {
		c.mu.Unlock()
		return ErrUnavailable
	}
	selected.State = "sending"
	selected.Attempts++
	selected.LastAttemptAt = &now
	next.Deliveries[selected.ID] = *selected
	if err := c.persist(ctx, next); err != nil {
		c.mu.Unlock()
		return err
	}
	channel := next.Channels[selected.ChannelID]
	secret := ChannelSecret{ID: channel.ID, Type: channel.Type, Config: map[string]string{}}
	for k, v := range channel.Config {
		secret.Config[k] = v
	}
	sender := c.sender
	c.inFlight[selected.ID] = true
	c.mu.Unlock()
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	outcome, sendErr := safeSend(sender, sendCtx, secret, selected.Payload)
	cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inFlight, selected.ID)
	next = cloneState(c.state)
	d, ok := next.Deliveries[selected.ID]
	if !ok {
		return errors.New("claimed delivery disappeared")
	}
	now = c.now()
	switch {
	case sendErr == nil:
		d.State = "sent"
		d.DeliveredAt = &now
		d.ErrorCode = ""
	case outcome.Uncertain || ctx.Err() != nil:
		d.State = "uncertain"
		d.ErrorCode = "delivery-outcome-unknown"
	case d.Attempts >= 5:
		d.State = "failed"
		d.ErrorCode = "delivery-failed"
	default:
		d.State = "retrying"
		d.ErrorCode = "delivery-retry"
		delay := time.Duration(1<<uint(d.Attempts)) * time.Second
		if outcome.RetryAfter > delay {
			delay = outcome.RetryAfter
		}
		if delay > time.Hour {
			delay = time.Hour
		}
		d.NextAttemptAt = now.Add(delay)
	}
	next.Deliveries[d.ID] = d
	return c.persist(ctx, next)
}
func (c *Center) Run(ctx context.Context) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.running = false; c.mu.Unlock() }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.DeliverOnce(ctx)
		}
	}
}

func safeSend(sender Sender, ctx context.Context, secret ChannelSecret, n Notification) (out DeliveryOutcome, err error) {
	defer func() {
		if recover() != nil {
			out.Uncertain = true
			err = errors.New("delivery transport interrupted")
		}
	}()
	return sender.Send(ctx, secret, n)
}

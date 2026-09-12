package alertcenter

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"talosdeck/internal/clusters"
)

type memoryStore struct {
	mu   sync.Mutex
	raw  map[string][]byte
	fail bool
}

func (s *memoryStore) GetSecret(_ context.Context, scope, kind, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.raw[scope+kind+key]
	if !ok {
		return nil, clusters.ErrNotFound
	}
	return append([]byte(nil), raw...), nil
}
func (s *memoryStore) PutSecret(_ context.Context, scope, kind, key string, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("secret-storage-error-do-not-expose")
	}
	s.raw[scope+kind+key] = append([]byte(nil), raw...)
	return nil
}

type recordingSender struct {
	mu      sync.Mutex
	events  []Notification
	outcome DeliveryOutcome
	err     error
}

func (s *recordingSender) Send(_ context.Context, _ ChannelSecret, n Notification) (DeliveryOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, n)
	return s.outcome, s.err
}
func fixture(t *testing.T) (*Center, *memoryStore, *recordingSender) {
	t.Helper()
	store := &memoryStore{raw: map[string][]byte{}}
	sender := &recordingSender{}
	c, err := Open(context.Background(), store, "fixture-cluster", sender)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return c, store, sender
}
func route(t *testing.T, c *Center, resolved bool) Channel {
	t.Helper()
	channel, err := c.SaveChannel(context.Background(), ChannelInput{Name: "fixture", Type: "webhook", Enabled: true, Config: map[string]string{"url": "https://example.invalid/hook"}})
	if err != nil {
		t.Fatal(err)
	}
	err = c.SaveRoutes(context.Background(), RoutesConfig{Routes: []Route{{Name: "all", Enabled: true, MinSeverity: "info", ChannelIDs: []string{channel.ID}, SendResolved: resolved}}})
	if err != nil {
		t.Fatal(err)
	}
	return channel
}
func observation(active bool) Observation {
	return Observation{RuleID: "node.ready", ResourceID: "node-uid", Node: "10.0.0.1", Component: "nodes", Known: true, Active: active, Severity: "critical", Title: "Node NotReady", Details: "Read-only condition"}
}
func TestPersistentLifecycleAndFirstFailureDedup(t *testing.T) {
	ctx := context.Background()
	c, store, sender := fixture(t)
	route(t, c, true)
	o := observation(true)
	if err := c.Observe(ctx, []Observation{o}); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().Summary.Active != 1 || len(c.Deliveries()) != 1 {
		t.Fatal("first unhealthy observation lost")
	}
	if err := c.Observe(ctx, []Observation{o}); err != nil {
		t.Fatal(err)
	}
	if len(c.Deliveries()) != 1 {
		t.Fatal("identical observation queued duplicate")
	}
	if err := c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, store, "fixture-cluster", sender)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = c.now
	if err = reopened.Observe(ctx, []Observation{o}); err != nil {
		t.Fatal(err)
	}
	if err = reopened.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 1 {
		t.Fatal("restart resent first failure")
	}
	o.Known = false
	if err = reopened.Observe(ctx, []Observation{o}); err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().Summary.Stale != 1 || reopened.Snapshot().Summary.Active != 1 {
		t.Fatal("unknown resolved active incident")
	}
	if err = reopened.Observe(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().Summary.Active != 1 {
		t.Fatal("absence resolved incident")
	}
	o = observation(false)
	if err = reopened.Observe(ctx, []Observation{o}); err != nil {
		t.Fatal(err)
	}
	if err = reopened.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 2 || sender.events[1].State != "resolved" {
		t.Fatal("verified recovery not delivered")
	}
	reopened.now = func() time.Time { return c.now().Add(time.Minute) }
	if err = reopened.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	if len(reopened.Snapshot().Alerts) != 2 || reopened.Snapshot().Summary.Active != 1 {
		t.Fatal("reopened incident history lost")
	}
}
func TestPersistenceFailureNeverPublishesOrSends(t *testing.T) {
	ctx := context.Background()
	c, store, sender := fixture(t)
	route(t, c, true)
	store.fail = true
	if err := c.Observe(ctx, []Observation{observation(true)}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected durable error got%v", err)
	}
	if len(c.Snapshot().Alerts) != 0 {
		t.Fatal("failed state published")
	}
	if err := c.DeliverOnce(ctx); err == nil {
		t.Fatal("failed store considered healthy")
	}
	if len(sender.events) != 0 {
		t.Fatal("sent uncommitted observation")
	}
	store.fail = false
	if err := c.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if err := c.DeliverOnce(ctx); err == nil {
		t.Fatal("failed claim succeeded")
	}
	if len(sender.events) != 0 {
		t.Fatal("transport called before durable claim")
	}
}
func TestSilenceRecoverySuppressesPendingAndGenerationChange(t *testing.T) {
	ctx := context.Background()
	c, _, sender := fixture(t)
	channel := route(t, c, true)
	silence, err := c.AddSilence(ctx, Silence{RuleID: "node.ready", EndsAt: c.now().Add(time.Hour), Reason: "maintenance", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	if err = c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 0 || c.Snapshot().Summary.Silenced != 1 {
		t.Fatal("silence did not suppress delivery")
	}
	if err = c.Observe(ctx, []Observation{observation(false)}); err != nil {
		t.Fatal(err)
	}
	if err = c.DeleteSilence(ctx, silence.ID); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return silence.EndsAt.Add(time.Second) }
	if err = c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, n := range sender.events {
		if n.State == "active" {
			t.Fatal("stale active sent after recovery")
		}
	}
	if err = c.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	_, err = c.SaveChannel(ctx, ChannelInput{ID: channel.ID, Generation: channel.Generation, Name: "changed-recipient", Type: "webhook", Enabled: true, Config: map[string]string{"url": "https://other.invalid/hook"}})
	if err != nil {
		t.Fatal(err)
	}
	before := len(sender.events)
	if err = c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != before {
		t.Fatal("old queued event sent after recipient changed")
	}
}
func TestJobSuccessEventAndSecretRedaction(t *testing.T) {
	ctx := context.Background()
	c, _, sender := fixture(t)
	route(t, c, false)
	o := observation(false)
	o.Event = true
	o.RuleID = "job.succeeded"
	o.ResourceID = "job-id"
	o.Title = "Operation succeeded"
	o.Details = "token=TOPSECRET"
	if err := c.Observe(ctx, []Observation{o}); err != nil {
		t.Fatal(err)
	}
	if err := c.Observe(ctx, []Observation{o}); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().Summary.Active != 0 || len(c.Snapshot().Alerts) != 1 || len(c.Deliveries()) != 1 {
		t.Fatal("success became active or duplicated")
	}
	if err := c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 1 || sender.events[0].State != "resolved" {
		t.Fatal("event depended on sendResolved")
	}
	raw, _ := json.Marshal(c.Snapshot())
	if string(raw) == "" || contains(string(raw), "TOPSECRET") {
		t.Fatal("sensitive observation escaped whitelist redaction")
	}
}
func contains(s, fragment string) bool {
	for i := 0; i+len(fragment) <= len(s); i++ {
		if s[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
func TestUncertainDeliveryRequiresExplicitRetryAndRestartClaim(t *testing.T) {
	ctx := context.Background()
	c, store, sender := fixture(t)
	route(t, c, true)
	sender.err = errors.New("secret-url-error")
	sender.outcome.Uncertain = true
	if err := c.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	d := c.Deliveries()[0]
	if d.State != "uncertain" {
		t.Fatal(d)
	}
	if err := c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 1 {
		t.Fatal("uncertain delivery retried automatically")
	}
	if err := c.RetryDelivery(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	sender.err = nil
	sender.outcome.Uncertain = false
	if err := c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 2 || c.Deliveries()[0].State != "sent" {
		t.Fatal("explicit retry failed")
	}
	c.mu.Lock()
	s := cloneState(c.state)
	claim := s.Deliveries[d.ID]
	claim.State = "sending"
	s.Deliveries[d.ID] = claim
	err := c.persist(ctx, s)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, store, "fixture-cluster", sender)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Deliveries()[0].State != "uncertain" {
		t.Fatal("restart replayed ambiguous sending")
	}
}
func TestChannelMetadataOptimisticUpdatesAndLegacyMigrationOnce(t *testing.T) {
	ctx := context.Background()
	c, store, sender := fixture(t)
	channel := route(t, c, true)
	updated, err := c.SaveChannel(ctx, ChannelInput{ID: channel.ID, Generation: channel.Generation, Name: "renamed", Type: "webhook", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Generation != 2 {
		t.Fatal("generation missing")
	}
	if _, err = c.SaveChannel(ctx, ChannelInput{ID: channel.ID, Generation: 1, Name: "stale", Type: "webhook", Enabled: true}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale update accepted")
	}
	if err = c.ImportLegacyTelegram(ctx, "123:valid-token", "123", "warning", false); err != nil {
		t.Fatal(err)
	}
	count := len(c.Channels())
	reopened, err := Open(ctx, store, "fixture-cluster", sender)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.ImportLegacyTelegram(ctx, "other-token", "other-chat", "info", true); err != nil {
		t.Fatal(err)
	}
	if len(reopened.Channels()) != count {
		t.Fatal("legacy migration repeated")
	}
	raw, _ := json.Marshal(c.Channels())
	if contains(string(raw), "https://") || contains(string(raw), "valid-token") {
		t.Fatal("channel secrets exposed")
	}
}
func TestScopeIsolationAndSilenceValidation(t *testing.T) {
	ctx := context.Background()
	c, store, sender := fixture(t)
	if err := c.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, store, "other-cluster", sender)
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Snapshot().Alerts) != 0 {
		t.Fatal("cross-cluster incidents")
	}
	if _, err = c.AddSilence(ctx, Silence{EndsAt: c.now().Add(time.Hour), Reason: "all", CreatedBy: "admin"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("selector-free silence accepted")
	}
}

func TestSilenceDeletionReleasesQueueImmediately(t *testing.T) {
	ctx := context.Background()
	c, _, sender := fixture(t)
	route(t, c, false)
	q, err := c.AddSilence(ctx, Silence{Node: "10.0.0.1", EndsAt: c.now().Add(time.Hour), Reason: "maintenance", CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	if err = c.DeleteSilence(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if err = c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 1 {
		t.Fatal("deleted silence left notification delayed")
	}
}
func TestRepeatDeliveryAndDurableDedupWatermark(t *testing.T) {
	ctx := context.Background()
	c, store, sender := fixture(t)
	route(t, c, false)
	routes := c.Routes()
	routes.Routes[0].RepeatMinutes = 5
	if err := c.SaveRoutes(ctx, routes); err != nil {
		t.Fatal(err)
	}
	if err := c.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	now := c.now().Add(6 * time.Minute)
	c.now = func() time.Time { return now }
	if err := c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 2 {
		t.Fatal("reminder missing")
	}
	if err := c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 2 {
		t.Fatal("same reminder slot repeated")
	}
	c.mu.Lock()
	next := cloneState(c.state)
	next.Deliveries = map[string]storedDelivery{}
	err := c.persist(ctx, next)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, store, "fixture-cluster", sender)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = c.now
	if err = reopened.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	if err = reopened.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 2 {
		t.Fatal("history pruning/restart resent initial delivery")
	}
}

type failAfterSend struct {
	store *memoryStore
	count int
}

func (s *failAfterSend) Send(context.Context, ChannelSecret, Notification) (DeliveryOutcome, error) {
	s.count++
	s.store.mu.Lock()
	s.store.fail = true
	s.store.mu.Unlock()
	return DeliveryOutcome{}, nil
}
func TestCompletionPersistenceFailureBecomesUncertainWithoutReplay(t *testing.T) {
	ctx := context.Background()
	c, store, _ := fixture(t)
	sender := &failAfterSend{store: store}
	c.sender = sender
	route(t, c, false)
	if err := c.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeliverOnce(ctx); err == nil {
		t.Fatal("completion persistence failure hidden")
	}
	store.mu.Lock()
	store.fail = false
	store.mu.Unlock()
	if err := c.DeliverOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if sender.count != 1 || c.Deliveries()[0].State != "uncertain" {
		t.Fatal("successful but uncommitted transport replayed")
	}
}

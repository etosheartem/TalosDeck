package alertcenter

import (
	"context"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"talosdeck/internal/clusters"
	"testing"
)

func TestEncryptedAggregateSurvivesStoreReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, key := filepath.Join(dir, "state.db"), filepath.Join(dir, "master.key")
	store, err := clusters.Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	scope := uuid.NewString()
	c, err := Open(ctx, store, scope, &recordingSender{})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := c.SaveChannel(ctx, ChannelInput{Name: "private-receiver", Type: "webhook", Enabled: true, Config: map[string]string{"url": "https://example.invalid/ULTRA_PRIVATE_WEBHOOK_PATH", "hmacSecret": "ULTRA_PRIVATE_SIGNING_SECRET"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.SaveRoutes(ctx, RoutesConfig{Routes: []Route{{Name: "route", Enabled: true, MinSeverity: "info", ChannelIDs: []string{channel.ID}}}}); err != nil {
		t.Fatal(err)
	}
	if err = c.Observe(ctx, []Observation{observation(true)}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"ULTRA_PRIVATE_WEBHOOK_PATH", "ULTRA_PRIVATE_SIGNING_SECRET", "private-receiver"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("alert aggregate stored plaintext")
		}
	}
	store, err = clusters.Open(db, key)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened, err := Open(ctx, store, scope, &recordingSender{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Channels()) != 1 || len(reopened.Deliveries()) != 1 || reopened.Snapshot().Summary.Active != 1 || reopened.Snapshot().Summary.Stale != 1 {
		t.Fatal("atomic lifecycle/outbox failed to survive restart")
	}
}

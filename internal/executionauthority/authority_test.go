package executionauthority

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func connectFixture(t *testing.T, dir, instance string) (*Client, error) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); defer server.Close(); _ = Serve(context.Background(), dir, server, server) }()
	var expected uint64
	if raw, err := os.ReadFile(filepath.Join(dir, "epoch")); err == nil {
		expected, _ = strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	}
	return openTransport(context.Background(), client, client, func() { server.Close(); <-done }, instance, expected)
}
func TestExclusiveExternalLeaseAndOldClientNeverReconnects(t *testing.T) {
	dir := t.TempDir()
	old, err := connectFixture(t, dir, "old")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err = old.Validate(context.Background(), "old", old.Epoch); err != nil {
		t.Fatal(err)
	}
	if second, err := connectFixture(t, dir, "new"); err == nil {
		second.Close()
		t.Fatal("simultaneous lease acquired")
	}
	oldEpoch := old.Epoch
	old.Close()
	fresh, err := connectFixture(t, dir, "new")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if fresh.Epoch <= oldEpoch {
		t.Fatal("epoch did not advance")
	}
	if err = old.Validate(context.Background(), "old", oldEpoch); err == nil {
		t.Fatal("old client reconnected")
	}
	if err = fresh.Validate(context.Background(), "new", fresh.Epoch); err != nil {
		t.Fatal(err)
	}
}
func TestReplacedLockPoisonsLease(t *testing.T) {
	dir := t.TempDir()
	c, err := connectFixture(t, dir, "instance")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = os.Rename(filepath.Join(dir, "execution.lock"), filepath.Join(dir, "old.lock")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "execution.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = c.Validate(context.Background(), c.InstanceID, c.Epoch); err == nil {
		t.Fatal("replaced lock accepted")
	}
}
func TestCorruptEpochCannotResetToOne(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "epoch"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := connectFixture(t, dir, "instance"); err == nil {
		c.Close()
		t.Fatal("corrupt epoch reset")
	}
}
func TestSSHRejectsCommandInjection(t *testing.T) {
	_, err := OpenSSH(context.Background(), SSHOptions{Host: "host; touch /tmp/unsafe", RemoteBinary: "/bin/helper", StateDir: "/data/authority"}, "instance")
	if err == nil {
		t.Fatal("unsafe host accepted")
	}
}

func TestSupersededSnapshotCannotAcquireAfterNewHolderExits(t *testing.T) {
	dir := t.TempDir()
	first, err := connectFixture(t, dir, "first")
	if err != nil {
		t.Fatal(err)
	}
	oldEpoch := first.Epoch
	first.Close()
	next, err := connectFixture(t, dir, "next")
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
	local, remote := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); defer remote.Close(); _ = Serve(context.Background(), dir, remote, remote) }()
	stale, err := openTransport(context.Background(), local, local, func() { remote.Close(); <-done }, "returned", oldEpoch)
	if err == nil {
		stale.Close()
		t.Fatal("superseded data acquired after new holder exited")
	}
}

func TestClientRejectsHelperIgnoringExpectedEpoch(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		var hello message
		_ = json.NewDecoder(server).Decode(&hello)
		_ = json.NewEncoder(server).Encode(message{Op: "acquired", Instance: hello.Instance, Epoch: 99})
	}()
	c, err := openTransport(context.Background(), client, client, func() { server.Close(); <-done }, "stale-instance", 42)
	if err == nil {
		c.Close()
		t.Fatal("incompatible helper silently adopted a newer epoch")
	}
}

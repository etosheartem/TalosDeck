package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"talosdeck/internal/reconcile"
	"testing"
	"time"
)

type fenceFixture struct {
	running            bool
	slow               bool
	owned              OwnedMachine
	ha                 bool
	outage             bool
	mismatch           bool
	audit              bool
	exists             bool
	loseDeleteResponse bool
	writes             int
	task               string
}

func fenceTestClient(t *testing.T) (*Client, *fenceFixture) {
	t.Helper()
	f := &fenceFixture{owned: NewOwnership("provider", "cluster", "plan", "pve", testMachine(), 150).Machine(), audit: true, exists: true, task: "UPID:pve:000001:000001:000001:qmdestroy:150:root@pam:"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.slow {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		reply := func(v any) { json.NewEncoder(w).Encode(map[string]any{"data": v}) }
		if f.outage {
			w.WriteHeader(403)
			return
		}
		if r.Method != "GET" {
			f.writes++
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/config"):
			desc := ownershipMarker(f.owned)
			if f.mismatch {
				desc = "another owner"
			}
			reply(map[string]any{"name": f.owned.Name, "description": desc, "smbios1": "uuid=" + f.owned.ID, "net0": "virtio=" + f.owned.MAC})
		case r.URL.Path == "/api2/json/cluster/ha/resources":
			if f.ha {
				reply([]any{map[string]any{"sid": "vm:150"}})
			} else {
				reply([]any{})
			}
		case r.URL.Path == "/api2/json/access/permissions":
			value := 0
			if f.audit {
				value = 1
			}
			reply(map[string]any{"/vms/150": map[string]any{"VM.Audit": value}})
		case strings.Contains(r.URL.Path, "/tasks/"):
			reply(map[string]any{"upid": f.task, "node": "pve", "type": "qmdestroy", "status": "stopped", "exitstatus": "OK"})
		case strings.HasSuffix(r.URL.Path, "/status/current"):
			status := "stopped"
			if f.running {
				status = "running"
			}
			reply(map[string]any{"status": status})
		case strings.HasSuffix(r.URL.Path, "/status/stop"):
			f.running = false
			reply(f.task)
		case r.Method == "DELETE":
			f.exists = false
			if f.loseDeleteResponse {
				w.WriteHeader(504)
				return
			}
			reply(f.task)
		case r.URL.Path == "/api2/json/cluster/resources":
			if f.exists {
				reply([]any{map[string]any{"vmid": 150, "type": "qemu"}})
			} else {
				reply([]any{})
			}
		default:
			t.Errorf("unexpected provider route: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{BaseURL: server.URL, Node: "pve", APIToken: "root@pam!test=value"})
	if err != nil {
		t.Fatal(err)
	}
	return client, f
}
func TestDestroyFenceRequiresOwnershipHAAndAuditBeforeMutation(t *testing.T) {
	for _, kind := range []string{"ha", "permission", "ownership", "outage"} {
		t.Run(kind, func(t *testing.T) {
			c, f := fenceTestClient(t)
			switch kind {
			case "ha":
				f.ha = true
			case "permission":
				f.audit = false
			case "ownership":
				f.mismatch = true
			case "outage":
				f.outage = true
			}
			e, err := c.DestroyOwnedForFence(fenceGuardContext(), f.owned)
			if err == nil || f.writes != 0 || e.Outcome != "UNKNOWN" {
				t.Fatal("unsafe fencing accepted", e, err, f.writes)
			}
		})
	}
}
func TestDestroyFenceAndFreshRevalidationNeverDeleteReusedVMID(t *testing.T) {
	c, f := fenceTestClient(t)
	ctx := fenceGuardContext()
	e, err := c.DestroyOwnedForFence(ctx, f.owned)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Fresh(time.Now()) || e.Fresh(time.Now().Add(61*time.Second)) || f.writes != 1 {
		t.Fatal(e, f.writes)
	}
	e.VerifiedAt = time.Now().Add(-2 * time.Minute)
	e, err = c.RevalidateWorkerFence(ctx, f.owned, e)
	if err != nil || !e.Fresh(time.Now()) || f.writes != 1 {
		t.Fatal("revalidation mutated provider", e, err, f.writes)
	}
	f.exists = true
	blocked, err := c.RevalidateWorkerFence(ctx, f.owned, e)
	if err == nil || blocked.Outcome != "UNKNOWN" || blocked.Fresh(time.Now()) || f.writes != 1 {
		t.Fatal("reused VMID accepted or deleted", blocked, err, f.writes)
	}
	f.exists = false
	f.audit = false
	unknown, err := c.RevalidateWorkerFence(ctx, f.owned, e)
	if err == nil || unknown.Outcome != "UNKNOWN" {
		t.Fatal("filtered absence trusted", unknown, err)
	}
}
func TestAmbiguousFenceDeleteDoesNotClaimStoppedOrRetry(t *testing.T) {
	c, f := fenceTestClient(t)
	f.loseDeleteResponse = true
	e, err := c.DestroyOwnedForFence(fenceGuardContext(), f.owned)
	if err == nil || e.Outcome != "UNKNOWN" || f.writes != 1 || f.exists {
		t.Fatal(e, err)
	}
	if _, err = c.RevalidateWorkerFence(fenceGuardContext(), f.owned, e); err == nil || f.writes != 1 {
		t.Fatal("unknown deletion task replayed")
	}
}
func TestFenceCancelledProviderRequestRemainsUnknown(t *testing.T) {
	c, f := fenceTestClient(t)
	ctx, cancel := context.WithCancel(fenceGuardContext())
	cancel()
	e, err := c.DestroyOwnedForFence(ctx, f.owned)
	if err == nil || e.Outcome != "UNKNOWN" || f.writes != 0 {
		t.Fatal(e, err, f.writes)
	}
}

func TestFenceProviderTimeoutRemainsUnknown(t *testing.T) {
	c, f := fenceTestClient(t)
	f.slow = true
	ctx, cancel := context.WithTimeout(fenceGuardContext(), 10*time.Millisecond)
	defer cancel()
	e, err := c.DestroyOwnedForFence(ctx, f.owned)
	if err == nil || e.Outcome != "UNKNOWN" || f.writes != 0 {
		t.Fatal(e, err, f.writes)
	}
}

func fenceGuardContext() context.Context {
	return reconcile.WithMutationGuard(context.Background(), func(context.Context) error { return nil })
}
func TestFenceRechecksAuthorityBetweenStopAndDelete(t *testing.T) {
	c, f := fenceTestClient(t)
	f.running = true
	checks := 0
	ctx := reconcile.WithMutationGuard(context.Background(), func(context.Context) error {
		checks++
		if checks > 1 {
			return errors.New("lease lost")
		}
		return nil
	})
	evidence, err := c.DestroyOwnedForFence(ctx, f.owned)
	if err == nil || checks != 2 || f.writes != 1 || f.running || !f.exists || evidence.Outcome != "UNKNOWN" {
		t.Fatal("deletion proceeded after losing execution authority", checks, f.writes, evidence, err)
	}
}
func TestFenceWithoutWorkflowAdmissionCannotMutate(t *testing.T) {
	c, f := fenceTestClient(t)
	e, err := c.DestroyOwnedForFence(context.Background(), f.owned)
	if err == nil || f.writes != 0 || e.Outcome != "UNKNOWN" {
		t.Fatal("unguarded mutation accepted", e, err)
	}
}

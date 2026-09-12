package operations

import (
	"context"
	"errors"
	"testing"

	"talosdeck/internal/jobs"
)

type settlingComponents struct {
	*fixture
	transientFailures int
}

func (f *settlingComponents) VerifyUpgradeComponents(context.Context, string, bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// The upgraded node is already Ready, while its static control-plane pods
	// need two further observations to stabilize after the reboot.
	if len(f.commands) == 1 && f.transientFailures < 2 {
		f.transientFailures++
		return errors.New("controller-manager is not ready yet")
	}
	return nil
}

func TestRollingUpgradeWaitsForControlPlaneComponentsToSettle(t *testing.T) {
	f := &settlingComponents{fixture: newFixture()}
	s := service(f.fixture)
	s.Kubernetes = f
	j := runJob(t, s, jobs.Request{Kind: "talos-upgrade", Version: "1.14.0", AllowDowntime: true})
	if j.Status != "succeeded" {
		t.Fatalf("normal post-reboot settling failed the upgrade: %s", j.Error)
	}
	if f.transientFailures != 2 || len(f.commands) != 3 {
		t.Fatalf("expected readiness retries before remaining nodes: failures=%d commands=%d", f.transientFailures, len(f.commands))
	}
	for _, args := range f.commands {
		endpoint := ""
		for i, arg := range args {
			if arg == "--endpoints" && i+1 < len(args) {
				endpoint = args[i+1]
			}
		}
		if endpoint != args[5] {
			t.Fatalf("disruptive operation must reconnect directly to its node, got %v", args)
		}
	}
}

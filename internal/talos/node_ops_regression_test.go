package talos

import (
	"context"
	"testing"
	"time"

	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TALOS-28: a line split across gRPC chunks must arrive as one line.
func TestStreamLines_ReassemblesAcrossChunks(t *testing.T) {
	chunks := [][]byte{
		[]byte("first line\nsecond li"),
		[]byte("ne continues here\nthird\n"),
		[]byte("trailing without newline"),
	}

	logChan := make(chan string, 16)
	idx := 0
	err := streamLines(context.Background(), func() ([]byte, error) {
		if idx >= len(chunks) {
			return nil, context.Canceled
		}
		c := chunks[idx]
		idx++
		return c, nil
	}, logChan)
	if err != nil {
		t.Fatalf("streamLines returned error: %v", err)
	}
	close(logChan)

	var got []string
	for line := range logChan {
		got = append(got, line)
	}

	want := []string{"first line", "second line continues here", "third", "trailing without newline"}
	if len(got) != len(want) {
		t.Fatalf("expected %d lines, got %d: %q", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

// TALOS-32: a digest reference carries no version and must not surface as one.
func TestKubernetesVersionFromImage(t *testing.T) {
	cases := []struct {
		image string
		want  string
	}{
		{"registry.k8s.io/kubelet:v1.32.2", "v1.32.2"},
		{"registry.k8s.io:5000/kubelet:v1.32.2", "v1.32.2"},
		{"registry.k8s.io/kubelet@sha256:deadbeefcafe", ""},
		{"registry.k8s.io:5000/kubelet", ""},
		{"kubelet", ""},
		{"", ""},
	}

	for _, tc := range cases {
		if got := kubernetesVersionFromImage(tc.image); got != tc.want {
			t.Errorf("kubernetesVersionFromImage(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}

// TALOS-27: one slow candidate must not consume the budget of the next.
func TestAttemptBudget_ClampsToRemaining(t *testing.T) {
	if d, ok := attemptBudget(context.Background(), 4*time.Second); !ok || d != 4*time.Second {
		t.Errorf("no deadline: expected full 4s budget, got %v (ok=%v)", d, ok)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d, ok := attemptBudget(ctx, 4*time.Second)
	if !ok || d > 2*time.Second {
		t.Errorf("expected budget clamped to <=2s, got %v (ok=%v)", d, ok)
	}

	expired, cancelExpired := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancelExpired()
	time.Sleep(10 * time.Millisecond)
	if _, ok := attemptBudget(expired, 4*time.Second); ok {
		t.Error("expected no budget once the deadline has passed")
	}
}

// TALOS-29: uptime and restarts come from real events, not constants.
func TestServiceRuntimeFromEvents(t *testing.T) {
	if uptime, restarts := serviceRuntimeFromEvents(nil); uptime != "" || restarts != 0 {
		t.Errorf("nil events: expected empty uptime and 0 restarts, got %q/%d", uptime, restarts)
	}

	events := &machine.ServiceEvents{Events: []*machine.ServiceEvent{
		{State: "Preparing", Ts: timestamppb.New(time.Now().Add(-4 * time.Hour))},
		{State: "Running", Ts: timestamppb.New(time.Now().Add(-3 * time.Hour))},
		{State: "Failed", Ts: timestamppb.New(time.Now().Add(-2 * time.Hour))},
		{State: "Running", Ts: timestamppb.New(time.Now().Add(-90 * time.Minute))},
	}}

	uptime, restarts := serviceRuntimeFromEvents(events)
	if restarts != 1 {
		t.Errorf("expected 1 restart from two Running transitions, got %d", restarts)
	}
	if uptime != "1h 30m" {
		t.Errorf("expected uptime measured from the latest Running event, got %q", uptime)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{25 * time.Hour, "1d 1h"},
		{90 * time.Minute, "1h 30m"},
		{45 * time.Second, "0m"},
		{-time.Hour, "0m"},
	}

	for _, tc := range cases {
		if got := formatDuration(tc.d); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// TALOS-25: node discovery must not invent addresses from another cluster.
func TestNewUnreachableNode_NoFabricatedIdentity(t *testing.T) {
	n := newUnreachableNode("10.42.0.110")

	if n.Hostname != "10.42.0.110" {
		t.Errorf("expected hostname to fall back to the IP, got %q", n.Hostname)
	}
	if n.Role != "worker" {
		t.Errorf("unreachable node role must not be guessed as controlplane, got %q", n.Role)
	}
	if n.Ready {
		t.Error("unreachable node must not report ready")
	}
	if n.ServicesSummary == nil {
		t.Fatal("ServicesSummary must never be nil (serializes as JSON null)")
	}
}

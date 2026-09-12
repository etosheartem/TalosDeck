package api

import (
	"context"
	"errors"
	"talosdeck/internal/alertcenter"
	"talosdeck/internal/talos"
	"testing"
)

type reviewedAlertTalos struct {
	nodes []*talos.NodeOverview
	err   error
}

func (f *reviewedAlertTalos) ListNodes(context.Context) ([]*talos.NodeOverview, error) {
	return f.nodes, f.err
}
func (f *reviewedAlertTalos) GetEtcdStatus(context.Context) (*talos.EtcdClusterStatus, error) {
	return &talos.EtcdClusterStatus{Healthy: true}, nil
}
func (f *reviewedAlertTalos) GetNodeDisks(context.Context, string) ([]*talos.DiskInfo, error) {
	return nil, nil
}
func findReviewedObservation(t *testing.T, rows []alertcenter.Observation, rule string) alertcenter.Observation {
	t.Helper()
	for _, r := range rows {
		if r.RuleID == rule {
			return r
		}
	}
	t.Fatal("missing rule", rule)
	return alertcenter.Observation{}
}
func TestAlertCollectorUnknownBreaksConsecutiveHighSamples(t *testing.T) {
	f := &reviewedAlertTalos{nodes: []*talos.NodeOverview{{IP: "192.0.2.1", Ready: true, CPUUsageKnown: true, CPUUsage: 95}}}
	m := &alertMonitor{talos: f}
	sample := func() alertcenter.Observation {
		return findReviewedObservation(t, m.collect(context.Background(), nil), "runtime.resource.cpu")
	}
	if sample().Active {
		t.Fatal("first sample triggered")
	}
	f.err = errors.New("inventory incomplete")
	r := sample()
	if r.Known || r.Active {
		t.Fatal("partial inventory treated as authoritative")
	}
	f.err = nil
	if sample().Active {
		t.Fatal("unknown sample did not break debounce")
	}
	if !sample().Active {
		t.Fatal("two complete high samples did not trigger")
	}
}
func TestAlertCollectorMissingNodeRetainsUnknownAlert(t *testing.T) {
	f := &reviewedAlertTalos{}
	m := &alertMonitor{talos: f}
	prior := []alertcenter.Alert{{RuleID: "runtime.node.ready", ResourceID: "192.0.2.1", Node: "192.0.2.1", State: "active", Severity: "critical"}}
	r := findReviewedObservation(t, m.collect(context.Background(), prior), "runtime.node.ready")
	if r.Known {
		t.Fatal("missing node resolved without deletion evidence")
	}
}

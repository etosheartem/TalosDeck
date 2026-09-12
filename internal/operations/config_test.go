package operations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	machineconfig "github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/config/validation"
	"talosdeck/internal/clusters"
	"talosdeck/internal/talos"
)

type configFixture struct {
	mu         sync.Mutex
	config     []byte
	applied    int
	failApply  bool
	panicApply bool
}

func (f *configFixture) ListNodes(context.Context) ([]*talos.NodeOverview, error) {
	return []*talos.NodeOverview{{IP: "10.0.0.1", Ready: true}}, nil
}
func (f *configFixture) GetNodeConfig(context.Context, string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.config...), nil
}
func (f *configFixture) ApplyNodeConfig(_ context.Context, _ string, data []byte, mode string, dry bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if dry {
		return nil
	}
	f.applied++
	if f.panicApply {
		panic("private configuration details")
	}
	if f.failApply {
		return errors.New("credential-value-should-not-be-exposed")
	}
	if mode != "staged" {
		f.config = append([]byte(nil), data...)
	}
	return nil
}

func TestConfigPanicMarksHistoryInterruptedAndHistoryWorksOffline(t *testing.T) {
	s, f, _ := setupConfig(t)
	ctx := context.Background()
	plan, err := s.Plan(ctx, "10.0.0.1", "apiVersion: v1alpha1\nkind: KubeNodeConfig\nlabels:\n  test: proposed\n", "auto", "alice")
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.Request(ctx, "10.0.0.1", plan.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	f.panicApply = true
	result := runJob(t, &Service{Config: s}, request)
	if result.Status != "interrupted" {
		t.Fatalf("%+v", result)
	}
	s.NodeClient = nil
	history, err := s.History(ctx, "10.0.0.1")
	if err != nil || history[0].Status != "interrupted" {
		t.Fatalf("%+v %v", history, err)
	}
	view, err := s.Revision(ctx, "10.0.0.1", plan.ID)
	if err != nil || view.ID != plan.ID {
		t.Fatalf("offline history: %+v %v", view, err)
	}
}

func TestRedactionCoversOpaqueConfigurationAndURLCredentials(t *testing.T) {
	value := map[string]any{"env": map[string]any{"ACCESS": "hidden-env"}, "content": "hidden-file", "httpHeaders": map[string]any{"X-Custom": "hidden-header"}, "endpoints": []any{"https://user:hidden-url@example.com/path?code=hidden-query"}, "name": "normal-name"}
	redactOpaque(value)
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), "hidden-") || !strings.Contains(string(encoded), "normal-name") {
		t.Fatalf("unsafe redaction: %s", encoded)
	}
}
func setupConfig(t *testing.T) (*ConfigService, *configFixture, string) {
	t.Helper()
	input, err := generate.NewInput("fixture", "https://10.0.0.1:6443", "1.34.0", generate.WithInstallDisk("/dev/sda"))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := input.Config(machineconfig.TypeControlPlane)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ValidateAsClient(configRuntimeMode{}, validation.WithStrict()); err != nil {
		t.Fatalf("fixture validation: %v", err)
	}
	data, err := provider.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := clusters.Open(filepath.Join(dir, "clusters.db"), filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cluster, err := store.Create(context.Background(), clusters.Cluster{Name: "fixture", Identity: "fixture-ca"}, clusters.Credentials{Talosconfig: []byte("credentials")})
	if err != nil {
		t.Fatal(err)
	}
	f := &configFixture{config: data}
	return &ConfigService{ClusterID: cluster.ID, Store: store, NodeClient: f}, f, dir
}
func TestConfigPlanApplyHistoryAndRestore(t *testing.T) {
	s, f, dir := setupConfig(t)
	ctx := context.Background()
	before := append([]byte(nil), f.config...)
	plan, err := s.Plan(ctx, "10.0.0.1", "apiVersion: v1alpha1\nkind: KubeNodeConfig\nlabels:\n  test: changed-host\n", "auto", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Diff, "+    hostname: changed-host") && !strings.Contains(plan.Diff, "changed-host") {
		t.Fatal(plan.Diff)
	}
	if f.applied != 0 {
		t.Fatal("planning mutated node")
	}
	if _, err := s.Request(ctx, "10.0.0.1", plan.ID, "other"); err == nil {
		t.Fatal("other author consumed plan")
	}
	request, err := s.Request(ctx, "10.0.0.1", plan.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	result := runJob(t, &Service{Config: s}, request)
	if result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	history, err := s.History(ctx, "10.0.0.1")
	if err != nil || len(history) != 2 {
		t.Fatalf("%+v %v", history, err)
	}
	view, err := s.Revision(ctx, "10.0.0.1", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.Config, "[REDACTED]") {
		t.Fatal("credentials not redacted")
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "PRIVATE KEY") || strings.Contains(string(encoded), "credential-value") {
		t.Fatal("raw secrets exposed")
	}
	// WAL and database must not contain either complete active configuration or a
	// recognizable changed value. Revision metadata intentionally contains node ID.
	for _, name := range []string{"clusters.db", "clusters.db-wal"} {
		raw, _ := os.ReadFile(filepath.Join(dir, name))
		if strings.Contains(string(raw), "changed-host") {
			t.Fatal("plaintext machine configuration on disk")
		}
	}
	baseline := history[len(history)-1]
	restore, err := s.RestorePlan(ctx, "10.0.0.1", baseline.ID, "auto", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(restore.Diff, "-    hostname: changed-host") && !strings.Contains(restore.Diff, "changed-host") {
		t.Fatal(restore.Diff)
	}
	request, err = s.Request(ctx, "10.0.0.1", restore.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	request.Kind = "config-restore"
	result = runJob(t, &Service{Config: s}, request)
	if result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	normalized, _ := canonicalConfig(before)
	if string(f.config) != string(normalized) {
		t.Fatal("restore did not restore baseline")
	}
	if _, err := s.Request(ctx, "10.0.0.1", plan.ID, "alice"); err == nil {
		t.Fatal("applied plan replay allowed")
	}
}
func TestConfigRejectsStalePlanAndForeignNode(t *testing.T) {
	s, f, _ := setupConfig(t)
	ctx := context.Background()
	if _, err := s.Plan(ctx, "10.0.0.9", "machine: {}", "auto", "alice"); err == nil {
		t.Fatal("foreign node accepted")
	}
	plan, err := s.Plan(ctx, "10.0.0.1", "apiVersion: v1alpha1\nkind: KubeNodeConfig\nlabels:\n  test: proposed\n", "auto", "alice")
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.Request(ctx, "10.0.0.1", plan.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	f.config = []byte(strings.Replace(string(f.config), "/dev/sda", "/dev/sdb", 1))
	result := runJob(t, &Service{Config: s}, request)
	if result.Status != "failed" || !strings.Contains(result.Error, "changed") || f.applied != 0 {
		t.Fatalf("%+v", result)
	}
}
func TestConfigInvalidPatchDoesNotLeakInput(t *testing.T) {
	s, _, _ := setupConfig(t)
	for _, patch := range []string{"machine: [credential-value-should-not-be-exposed", "machine:\n  type: credential-value-should-not-be-exposed"} {
		_, err := s.Plan(context.Background(), "10.0.0.1", patch, "auto", "alice")
		if err == nil || strings.Contains(err.Error(), "credential-value") {
			t.Fatalf("unsafe validation result: %v", err)
		}
	}
}
func TestConfigUnconfirmedApplyRequiresReviewAndStagedIsExplicit(t *testing.T) {
	for _, mode := range []string{"auto", "staged"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _ := setupConfig(t)
			plan, err := s.Plan(context.Background(), "10.0.0.1", "apiVersion: v1alpha1\nkind: KubeNodeConfig\nlabels:\n  test: proposed\n", mode, "alice")
			if err != nil {
				t.Fatal(err)
			}
			request, _ := s.Request(context.Background(), "10.0.0.1", plan.ID, "alice")
			f.failApply = mode == "auto"
			result := runJob(t, &Service{Config: s}, request)
			if mode == "auto" && (result.Status != "interrupted" || strings.Contains(result.Error, "credential-value")) {
				t.Fatalf("%+v", result)
			}
			history, err := s.History(context.Background(), "10.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			status := "staged"
			if mode == "auto" {
				status = "interrupted"
			}
			if history[0].Status != status {
				t.Fatalf("%+v", history)
			}
		})
	}
}

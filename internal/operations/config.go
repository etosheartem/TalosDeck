package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/configpatcher"
	"sigs.k8s.io/yaml"
	"talosdeck/internal/clusters"
	"talosdeck/internal/jobs"
	"talosdeck/internal/talos"
)

type RevisionStore interface {
	AddRevision(context.Context, clusters.Revision) (clusters.Revision, error)
	ListRevisions(context.Context, string, string) ([]clusters.Revision, error)
	GetRevision(context.Context, string, string, string) (clusters.Revision, error)
	UpdateRevisionStatus(context.Context, string, string, string, string, string) error
}
type ConfigClient interface {
	ListNodes(context.Context) ([]*talos.NodeOverview, error)
	GetNodeConfig(context.Context, string) ([]byte, error)
	ApplyNodeConfig(context.Context, string, []byte, string, bool) error
}
type ConfigService struct {
	ClusterID  string
	Store      RevisionStore
	NodeClient ConfigClient
	// Audit receives metadata only, never configuration, patches or SDK errors.
	Audit func(action, user, node, status, revision string)
}
type ConfigPlan struct {
	ID       string   `json:"id"`
	Node     string   `json:"node"`
	Mode     string   `json:"mode"`
	Diff     string   `json:"diff"`
	Warnings []string `json:"warnings"`
}
type ConfigRevisionView struct {
	clusters.Revision
	Diff   string `json:"diff"`
	Config string `json:"config"`
}
type configPayload struct {
	Before []byte `json:"before"`
	After  []byte `json:"after"`
}
type configRuntimeMode struct{}

func (configRuntimeMode) String() string        { return "metal" }
func (configRuntimeMode) RequiresInstall() bool { return true }
func (configRuntimeMode) InContainer() bool     { return false }

func validateMode(mode string) error {
	if mode != "auto" && mode != "reboot" && mode != "staged" {
		return errors.New("mode must be auto, reboot or staged")
	}
	return nil
}
func (s *ConfigService) checkNode(ctx context.Context, node string) error {
	if unavailable(s.Store) || unavailable(s.NodeClient) || s.ClusterID == "" {
		return errors.New("configuration operations unavailable")
	}
	addr, err := netip.ParseAddr(node)
	if err != nil {
		return errors.New("invalid node address")
	}
	nodes, err := s.NodeClient.ListNodes(ctx)
	if err != nil {
		return errors.New("cannot verify cluster node inventory")
	}
	for _, n := range nodes {
		if n == nil {
			continue
		}
		candidate, e := netip.ParseAddr(n.IP)
		if e == nil && candidate.Unmap() == addr.Unmap() {
			return nil
		}
	}
	return errors.New("node does not belong to this cluster")
}
func canonicalConfig(data []byte) ([]byte, error) {
	provider, err := configloader.NewFromBytes(data)
	if err != nil {
		return nil, errors.New("invalid Talos machine configuration")
	}
	return provider.Bytes()
}
func validateConfig(data []byte) error {
	provider, err := configloader.NewFromBytes(data)
	if err != nil {
		return errors.New("invalid Talos machine configuration")
	}
	// Deprecated fields may still be valid on an older managed Talos node. The
	// authenticated server dry-run is authoritative for that node's version.
	if _, err = provider.ValidateAsClient(configRuntimeMode{}); err != nil {
		return errors.New("machine configuration failed Talos validation; check patch structure and required fields")
	}
	return nil
}

// RedactedConfig uses SDK-aware credential redaction and removes opaque payloads
// (files, manifests, environment) which may themselves contain private data.
func RedactedConfig(data []byte) (string, error) {
	provider, err := configloader.NewFromBytes(data)
	if err != nil {
		return "", errors.New("cannot decode configuration for redaction")
	}
	redacted, err := provider.RedactSecrets("[REDACTED]").Bytes()
	if err != nil {
		return "", errors.New("cannot redact configuration")
	}
	documents := bytes.Split(redacted, []byte("\n---\n"))
	for i, doc := range documents {
		var value any
		if err := yaml.Unmarshal(doc, &value); err != nil {
			return "", errors.New("cannot redact configuration document")
		}
		redactOpaque(value)
		encoded, err := yaml.Marshal(value)
		if err != nil {
			return "", errors.New("cannot encode redacted configuration")
		}
		documents[i] = encoded
	}
	return string(bytes.Join(documents, []byte("---\n"))), nil
}
func redactOpaque(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "credential") || strings.Contains(lower, "key") || strings.Contains(lower, "header") || lower == "crt" || lower == "content" || lower == "contents" || lower == "env" || lower == "inlinemanifests" || lower == "authorization" || lower == "auth" || lower == "clientcertificate" {
				v[key] = "[REDACTED]"
			} else {
				if text, ok := child.(string); ok {
					v[key] = redactURL(text)
				}
				redactOpaque(child)
			}
		}
	case []any:
		for i, child := range v {
			if text, ok := child.(string); ok {
				v[i] = redactURL(text)
			}
			redactOpaque(child)
		}
	}
}

func redactURL(text string) string {
	parsed, err := url.Parse(text)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.User == nil && parsed.RawQuery == "") {
		return text
	}
	if parsed.User != nil {
		parsed.User = url.User("REDACTED")
	}
	if parsed.RawQuery != "" {
		parsed.RawQuery = "REDACTED"
	}
	return parsed.String()
}
func configDiff(before, after []byte) (string, error) {
	a, err := RedactedConfig(before)
	if err != nil {
		return "", err
	}
	b, err := RedactedConfig(after)
	if err != nil {
		return "", err
	}
	if a == b {
		return "Only redacted values or formatting changed.", nil
	}
	// Common prefix/suffix keeps a useful small contextual diff without quadratic
	// work on large machine configurations.
	left, right := strings.Split(strings.TrimSuffix(a, "\n"), "\n"), strings.Split(strings.TrimSuffix(b, "\n"), "\n")
	prefix := 0
	for prefix < len(left) && prefix < len(right) && left[prefix] == right[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(left)-prefix && suffix < len(right)-prefix && left[len(left)-1-suffix] == right[len(right)-1-suffix] {
		suffix++
	}
	start := max(0, prefix-3)
	var result strings.Builder
	result.WriteString("--- active\n+++ proposed\n")
	for _, line := range left[start:prefix] {
		result.WriteString(" " + line + "\n")
	}
	for _, line := range left[prefix : len(left)-suffix] {
		result.WriteString("-" + line + "\n")
	}
	for _, line := range right[prefix : len(right)-suffix] {
		result.WriteString("+" + line + "\n")
	}
	for _, line := range left[len(left)-suffix : min(len(left), len(left)-suffix+3)] {
		result.WriteString(" " + line + "\n")
	}
	return result.String(), nil
}

func (s *ConfigService) Plan(ctx context.Context, node, patch, mode, user string) (*ConfigPlan, error) {
	if err := validateMode(mode); err != nil {
		return nil, err
	}
	if len(patch) == 0 || len(patch) > 1024*1024 {
		return nil, errors.New("patch must contain between 1 byte and 1 MiB")
	}
	if err := s.checkNode(ctx, node); err != nil {
		return nil, err
	}
	before, err := s.NodeClient.GetNodeConfig(ctx, node)
	if err != nil {
		return nil, errors.New("cannot read active configuration")
	}
	p, err := configpatcher.LoadPatch([]byte(patch))
	if err != nil {
		return nil, errors.New("invalid strategic merge or JSON6902 patch")
	}
	output, err := configpatcher.Apply(configpatcher.WithBytes(before), []configpatcher.Patch{p})
	if err != nil {
		return nil, errors.New("patch cannot be applied to the active configuration")
	}
	after, err := output.Bytes()
	if err != nil {
		return nil, errors.New("cannot encode patched configuration")
	}
	return s.makePlan(ctx, node, before, after, mode, user)
}
func (s *ConfigService) makePlan(ctx context.Context, node string, before, after []byte, mode, user string) (*ConfigPlan, error) {
	if err := validateMode(mode); err != nil {
		return nil, err
	}
	before, err := canonicalConfig(before)
	if err != nil {
		return nil, err
	}
	after, err = canonicalConfig(after)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(before, after) {
		return nil, errors.New("configuration is unchanged")
	}
	if err = validateConfig(after); err != nil {
		return nil, err
	}
	// Server-side dry-run also checks the target node's actual runtime semantics.
	if err = s.NodeClient.ApplyNodeConfig(ctx, node, after, mode, true); err != nil {
		return nil, errors.New("Talos dry-run rejected the proposed configuration")
	}
	diff, err := configDiff(before, after)
	if err != nil {
		return nil, err
	}
	history, err := s.Store.ListRevisions(ctx, s.ClusterID, node)
	if err != nil {
		return nil, errors.New("cannot read configuration history")
	}
	if len(history) == 0 {
		baseline, _ := json.Marshal(configPayload{Before: before, After: before})
		if _, err = s.Store.AddRevision(ctx, clusters.Revision{ClusterID: s.ClusterID, Node: node, Author: user, Mode: "auto", Status: "succeeded", Config: baseline}); err != nil {
			return nil, errors.New("cannot save encrypted configuration baseline")
		}
	}
	payload, _ := json.Marshal(configPayload{Before: before, After: after})
	revision, err := s.Store.AddRevision(ctx, clusters.Revision{ClusterID: s.ClusterID, Node: node, Author: user, Mode: mode, Status: "planned", Config: payload})
	if err != nil {
		return nil, errors.New("cannot save encrypted configuration plan")
	}
	return &ConfigPlan{ID: revision.ID, Node: node, Mode: mode, Diff: diff, Warnings: []string{"Secrets and embedded file/manifest contents are hidden. A plan expires after 30 minutes and is rejected if the active configuration changes.", "Talos may reboot the node in auto or reboot mode. Staged configuration is applied on the next reboot."}}, nil
}
func (s *ConfigService) History(ctx context.Context, node string) ([]clusters.Revision, error) {
	if _, err := netip.ParseAddr(node); err != nil {
		return nil, err
	}
	revisions, err := s.Store.ListRevisions(ctx, s.ClusterID, node)
	if err != nil {
		return nil, errors.New("cannot read configuration history")
	}
	result := []clusters.Revision{}
	for _, revision := range revisions {
		if revision.Status != "planned" {
			revision.Config = nil
			result = append(result, revision)
		}
	}
	return result, nil
}
func (s *ConfigService) Revision(ctx context.Context, node, id string) (*ConfigRevisionView, error) {
	if _, err := netip.ParseAddr(node); err != nil {
		return nil, err
	}
	revision, err := s.Store.GetRevision(ctx, s.ClusterID, node, id)
	if err != nil {
		return nil, errors.New("configuration revision not found")
	}
	var payload configPayload
	if json.Unmarshal(revision.Config, &payload) != nil {
		return nil, errors.New("configuration revision is unreadable")
	}
	diff, err := configDiff(payload.Before, payload.After)
	if err != nil {
		return nil, err
	}
	config, err := RedactedConfig(payload.After)
	if err != nil {
		return nil, err
	}
	revision.Config = nil
	return &ConfigRevisionView{Revision: revision, Diff: diff, Config: config}, nil
}
func (s *ConfigService) Request(ctx context.Context, node, id, user string) (jobs.Request, error) {
	if err := s.checkNode(ctx, node); err != nil {
		return jobs.Request{}, err
	}
	revision, err := s.Store.GetRevision(ctx, s.ClusterID, node, id)
	if err != nil || revision.Status != "planned" || revision.Author != user || time.Since(revision.CreatedAt) > 30*time.Minute {
		return jobs.Request{}, errors.New("configuration plan is unavailable or expired; create a new plan")
	}
	return jobs.Request{Kind: "config-apply", Node: node, ConfigRevisionID: id}, nil
}
func (s *ConfigService) RestorePlan(ctx context.Context, node, id, mode, user string) (*ConfigPlan, error) {
	if err := s.checkNode(ctx, node); err != nil {
		return nil, err
	}
	revision, err := s.Store.GetRevision(ctx, s.ClusterID, node, id)
	if err != nil || (revision.Status != "succeeded" && revision.Status != "staged") {
		return nil, errors.New("only an applied revision can be restored")
	}
	var payload configPayload
	if json.Unmarshal(revision.Config, &payload) != nil {
		return nil, errors.New("configuration revision is unreadable")
	}
	current, err := s.NodeClient.GetNodeConfig(ctx, node)
	if err != nil {
		return nil, errors.New("cannot read active configuration")
	}
	return s.makePlan(ctx, node, current, payload.After, mode, user)
}
func (s *ConfigService) Run(ctx context.Context, e *jobs.Execution, r jobs.Request) (runErr error) {
	if err := s.checkNode(ctx, r.Node); err != nil {
		return err
	}
	revision, err := s.Store.GetRevision(ctx, s.ClusterID, r.Node, r.ConfigRevisionID)
	if err != nil || revision.Status != "planned" || time.Since(revision.CreatedAt) > 30*time.Minute {
		return errors.New("configuration plan unavailable or expired")
	}
	var payload configPayload
	if json.Unmarshal(revision.Config, &payload) != nil {
		return errors.New("configuration plan is unreadable")
	}
	if err := e.Checkpoint(ctx, "config-validation", "Checking current configuration against the approved plan"); err != nil {
		return err
	}
	current, err := s.NodeClient.GetNodeConfig(ctx, r.Node)
	if err != nil {
		return errors.New("cannot read current configuration")
	}
	current, err = canonicalConfig(current)
	if err != nil || sha256.Sum256(current) != sha256.Sum256(payload.Before) {
		return errors.New("active configuration changed; create a new plan")
	}
	if err = validateConfig(payload.After); err != nil {
		return err
	}
	if err = s.NodeClient.ApplyNodeConfig(ctx, r.Node, payload.After, revision.Mode, true); err != nil {
		return errors.New("Talos configuration dry-run failed")
	}
	if err = e.Checkpoint(ctx, "config-apply", "Applying the encrypted configuration revision"); err != nil {
		return err
	}
	if err = s.Store.UpdateRevisionStatus(ctx, s.ClusterID, r.Node, revision.ID, "running", ""); err != nil {
		return errors.New("cannot persist configuration status; nothing applied")
	}
	defer func() {
		panicValue := recover()
		if panicValue != nil {
			runErr = jobs.ErrUncertain
		}
		status, message := "succeeded", ""
		if revision.Mode == "staged" {
			status = "staged"
		}
		if runErr != nil {
			status, message = "failed", "Configuration execution failed; inspect node state before retrying"
		}
		if ctx.Err() != nil {
			status, message = "interrupted", "Server stopped before configuration completion was verified"
		}
		if errors.Is(runErr, jobs.ErrUncertain) || errors.Is(runErr, context.DeadlineExceeded) {
			status, message = "interrupted", "Configuration outcome was not verified; inspect node state"
		}
		finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Store.UpdateRevisionStatus(finalCtx, s.ClusterID, r.Node, revision.ID, status, message); err != nil {
			runErr = fmt.Errorf("configuration result could not be persisted; inspect node state: %w", context.Canceled)
		}
		if s.Audit != nil {
			s.Audit(r.Kind, revision.Author, r.Node, status, revision.ID)
		}
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	if err = s.NodeClient.ApplyNodeConfig(ctx, r.Node, payload.After, revision.Mode, false); err != nil {
		return jobs.ErrUncertain
	}
	// STAGED is an accepted pending reboot, never represented as active config.
	if revision.Mode != "staged" {
		waitCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
		for {
			active, readErr := s.NodeClient.GetNodeConfig(waitCtx, r.Node)
			if readErr == nil {
				normalized, normalizeErr := canonicalConfig(active)
				if normalizeErr == nil && sha256.Sum256(normalized) == sha256.Sum256(payload.After) {
					break
				}
			}
			select {
			case <-waitCtx.Done():
				return waitCtx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return e.Log("config-complete", "Configuration revision recorded; mode: "+revision.Mode)
}

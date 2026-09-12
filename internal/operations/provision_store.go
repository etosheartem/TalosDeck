package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"talosdeck/internal/clusters"
	"talosdeck/internal/proxmox"
)

const FleetScope = "__fleet__"

type ProvisionStore interface {
	PutSecret(context.Context, string, string, string, []byte) error
	CreateSecret(context.Context, string, string, string, []byte) error
	GetSecret(context.Context, string, string, string) ([]byte, error)
	DeleteSecret(context.Context, string, string, string) error
	ListSecretKeys(context.Context, string, string) ([]string, error)
}
type ProviderRecord struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Config    proxmox.Config `json:"config"`
	CreatedAt time.Time      `json:"createdAt"`
}
type ProviderView struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	BaseURL        string `json:"baseUrl"`
	Node           string `json:"node"`
	DefaultStorage string `json:"defaultStorage"`
	DefaultISO     string `json:"defaultISO"`
	DefaultBridge  string `json:"defaultBridge"`
	SkipTLSVerify  bool   `json:"skipTlsVerify"`
	Configured     bool   `json:"configured"`
}

func providerView(p ProviderRecord) ProviderView {
	return ProviderView{ID: p.ID, Name: p.Name, Kind: p.Kind, BaseURL: p.Config.BaseURL, Node: p.Config.Node, DefaultStorage: p.Config.DefaultStorage, DefaultISO: p.Config.DefaultISO, DefaultBridge: p.Config.DefaultBridge, SkipTLSVerify: p.Config.SkipTLSVerify, Configured: true}
}
func loadProvider(ctx context.Context, store ProvisionStore, id string) (ProviderRecord, error) {
	var p ProviderRecord
	data, err := store.GetSecret(ctx, FleetScope, "provider", id)
	if err != nil || json.Unmarshal(data, &p) != nil {
		return p, errors.New("provider unavailable")
	}
	return p, nil
}

// ProviderClient resolves encrypted provider settings for a selected cluster.
// It never consults environment variables or another cluster's credentials.
func ProviderClient(ctx context.Context, store ProvisionStore, id string) (*proxmox.Client, error) {
	p, err := loadProvider(ctx, store, id)
	if err != nil {
		return nil, err
	}
	return proxmox.NewClient(p.Config)
}
func ListProviders(ctx context.Context, store ProvisionStore) ([]ProviderView, error) {
	ids, err := store.ListSecretKeys(ctx, FleetScope, "provider")
	if err != nil {
		return nil, errors.New("cannot read providers")
	}
	result := []ProviderView{}
	for _, id := range ids {
		p, err := loadProvider(ctx, store, id)
		if err != nil {
			return nil, err
		}
		result = append(result, providerView(p))
	}
	return result, nil
}
func SaveProvider(ctx context.Context, store ProvisionStore, name, kind string, cfg proxmox.Config, verify bool) (ProviderView, error) {
	if strings.TrimSpace(name) == "" || len(name) > 100 || kind != "proxmox" {
		return ProviderView{}, errors.New("provider name and kind proxmox are required")
	}
	address, err := url.Parse(cfg.BaseURL)
	if err != nil || address.Scheme != "https" || address.Hostname() == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" {
		return ProviderView{}, errors.New("provider URL must be HTTPS without embedded credentials")
	}
	if cfg.CACertFile != "" {
		return ProviderView{}, errors.New("use an inline CA certificate instead of a server filesystem path")
	}
	if cfg.Node == "" || strings.ContainsAny(cfg.Node, "/\\\r\n") {
		return ProviderView{}, errors.New("Proxmox node is required")
	}
	if cfg.APIToken == "" && (cfg.Username == "" || cfg.Password == "") {
		return ProviderView{}, errors.New("provider credentials are required")
	}
	client, err := proxmox.NewClient(cfg)
	if err != nil {
		return ProviderView{}, errors.New("invalid Proxmox configuration")
	}
	cfg = client.PrivateConfig()
	if verify {
		if _, err := client.GetNodeStatus(ctx); err != nil {
			return ProviderView{}, errors.New("provider connection or storage validation failed")
		}
	}
	record := ProviderRecord{ID: uuid.NewString(), Name: name, Kind: kind, Config: cfg, CreatedAt: time.Now().UTC()}
	data, _ := json.Marshal(record)
	if err := store.CreateSecret(ctx, FleetScope, "provider", record.ID, data); err != nil {
		return ProviderView{}, errors.New("cannot save encrypted provider")
	}
	return providerView(record), nil
}
func ImportLegacyProvider(ctx context.Context, store ProvisionStore, cfg proxmox.Config) (string, error) {
	ids, err := store.ListSecretKeys(ctx, FleetScope, "provider")
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		record, err := loadProvider(ctx, store, id)
		if err != nil {
			return "", err
		}
		if record.Config.BaseURL == cfg.BaseURL && record.Config.Node == cfg.Node {
			return id, nil
		}
	}
	// File-based CA is resolved by the legacy migration before reaching this API.
	p, err := SaveProvider(ctx, store, "Migrated Proxmox", "proxmox", cfg, false)
	return p.ID, err
}
func DeleteProvider(ctx context.Context, store ProvisionStore, id string) error {
	plans, err := store.ListSecretKeys(ctx, FleetScope, "provision-plan")
	if err != nil {
		return errors.New("cannot verify provider operation references")
	}
	for _, planID := range plans {
		data, err := store.GetSecret(ctx, FleetScope, "provision-plan", planID)
		var plan provisionState
		if err != nil || json.Unmarshal(data, &plan) != nil {
			return errors.New("cannot verify provider operation references")
		}
		if plan.Spec.ProviderID == id && (plan.Status == "running" || (plan.Status == "planned" && time.Since(plan.CreatedAt) <= 30*time.Minute)) {
			return errors.New("provider has an active provisioning plan; finish the operation or wait for the plan to expire")
		}
	}
	records, err := ListOwnedMachines(ctx, store, "")
	if err != nil {
		return err
	}
	for _, m := range records {
		if m.ProviderID == id && m.Status != "deleted" {
			return errors.New("provider has registered machines; delete or migrate them first")
		}
	}
	return store.DeleteSecret(ctx, FleetScope, "provider", id)
}
func ListOwnedMachines(ctx context.Context, store ProvisionStore, clusterID string) ([]proxmox.OwnedMachine, error) {
	ids, err := store.ListSecretKeys(ctx, FleetScope, "machine")
	if err != nil {
		return nil, errors.New("cannot read registered machines")
	}
	result := []proxmox.OwnedMachine{}
	for _, id := range ids {
		record, err := loadOwned(ctx, store, id)
		if err != nil {
			return nil, err
		}
		if clusterID == "" || clusterID == FleetScope || record.ClusterID == clusterID {
			checker := ProvisionService{ClusterID: FleetScope, Store: store}
			_, cleanupErr := checker.cleanupRecord(ctx, ProvisionSpec{MachineID: record.ID, Name: record.Name})
			record.CleanupEligible = cleanupErr == nil
			result = append(result, record.OwnedMachine)
		}
	}
	return result, nil
}
func loadOwned(ctx context.Context, store ProvisionStore, id string) (proxmox.OwnedMachineRecord, error) {
	var record proxmox.OwnedMachineRecord
	data, err := store.GetSecret(ctx, FleetScope, "machine", id)
	if err != nil || json.Unmarshal(data, &record) != nil {
		return record, errors.New("registered machine not found")
	}
	return record, nil
}
func saveOwned(ctx context.Context, store ProvisionStore, record proxmox.OwnedMachineRecord, create bool) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if create {
		err = store.CreateSecret(ctx, FleetScope, "machine", record.ID, data)
	} else {
		err = store.PutSecret(ctx, FleetScope, "machine", record.ID, data)
	}
	if err != nil {
		return fmt.Errorf("cannot persist encrypted machine ownership")
	}
	return nil
}

var _ ProvisionStore = (*clusters.Store)(nil)

package talos

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/client/config"
)

// TalosManager manages connections and operations with the Talos cluster.
type TalosManager struct {
	client          *client.Client
	cfg             *config.Config
	talosconfigPath string
	endpoints       []string
	nodes           []string
	mu              sync.RWMutex
}

// NewTalosManager opens the given talosconfig and initializes a Talos client.
func NewTalosManager(talosconfigPath string, extraNodes ...string) (*TalosManager, error) {
	cfg, err := config.Open(talosconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open talosconfig at %s: %w", talosconfigPath, err)
	}

	ctx := context.Background()
	c, err := client.New(ctx, client.WithConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("failed to create Talos client: %w", err)
	}

	var endpoints []string
	var nodes []string

	if currentCtx, ok := cfg.Contexts[cfg.Context]; ok {
		endpoints = currentCtx.Endpoints
		nodes = currentCtx.Nodes
	}

	// Always ensure known cluster nodes are present
	knownNodes := []string{"10.42.0.110", "10.42.0.111", "10.42.0.112"}
	nodeSet := make(map[string]bool)
	for _, n := range nodes {
		if n != "" {
			nodeSet[n] = true
		}
	}
	for _, n := range extraNodes {
		if n != "" {
			nodeSet[n] = true
		}
	}
	for _, n := range knownNodes {
		nodeSet[n] = true
	}

	nodes = make([]string, 0, len(nodeSet))
	for n := range nodeSet {
		nodes = append(nodes, n)
	}

	return &TalosManager{
		client:          c,
		cfg:             cfg,
		talosconfigPath: talosconfigPath,
		endpoints:       endpoints,
		nodes:           nodes,
	}, nil
}

// Close terminates the Talos client connection.
func (m *TalosManager) Close() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// GetClient returns the raw Talos client.
func (m *TalosManager) GetClient() *client.Client {
	return m.client
}

// GetConfig returns the Talos config.
func (m *TalosManager) GetConfig() *config.Config {
	return m.cfg
}

// GetConfigPath returns the file path to talosconfig.
func (m *TalosManager) GetConfigPath() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.talosconfigPath
}

// GetRawConfig returns the raw YAML bytes of the active talosconfig.
func (m *TalosManager) GetRawConfig() ([]byte, error) {
	m.mu.RLock()
	path := m.talosconfigPath
	m.mu.RUnlock()

	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
	}
	if m.cfg != nil {
		return m.cfg.Bytes()
	}
	return nil, fmt.Errorf("no talosconfig loaded")
}

// GetEndpoints returns configured cluster endpoints.
func (m *TalosManager) GetEndpoints() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.endpoints
}

// GetConfiguredNodes returns configured node IPs.
func (m *TalosManager) GetConfiguredNodes() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nodes
}

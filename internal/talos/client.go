package talos

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

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
	metricsMu       sync.Mutex
	cpuSamples      map[string]cpuSnapshot
	discoveryMu     sync.Mutex
	discoveryAt     time.Time
	discoveredNodes []string
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

	// A talosconfig context may declare only endpoints. Fall back to those rather
	// than polling an empty node list — or inventing addresses of some other cluster.
	if len(nodeSet) == 0 {
		for _, ep := range endpoints {
			if ep != "" {
				nodeSet[ep] = true
			}
		}
	}

	nodes = make([]string, 0, len(nodeSet))
	for n := range nodeSet {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes) // map iteration is randomized; keep node order stable

	return &TalosManager{
		client:          c,
		cfg:             cfg,
		talosconfigPath: talosconfigPath,
		endpoints:       endpoints,
		nodes:           nodes,
		cpuSamples:      make(map[string]cpuSnapshot),
	}, nil
}

// Close terminates the Talos client connection thread-safely.
func (m *TalosManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		err := m.client.Close()
		m.client = nil
		return err
	}
	return nil
}

// GetClient returns the raw Talos client.
func (m *TalosManager) GetClient() *client.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.client
}

// GetConfig returns the Talos config.
func (m *TalosManager) GetConfig() *config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// GetClusterName returns the cluster name/context thread-safely.
func (m *TalosManager) GetClusterName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg != nil && m.cfg.Context != "" {
		return m.cfg.Context
	}
	return "talos-cluster"
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
	defer m.mu.RUnlock()

	if m.talosconfigPath != "" {
		data, err := os.ReadFile(m.talosconfigPath)
		if err == nil {
			return data, nil
		}
	}
	if m.cfg != nil {
		return m.cfg.Bytes()
	}
	return nil, fmt.Errorf("no talosconfig loaded")
}

// GetEndpoints returns configured cluster endpoints (defensive copy).
func (m *TalosManager) GetEndpoints() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.endpoints == nil {
		return nil
	}
	res := make([]string, len(m.endpoints))
	copy(res, m.endpoints)
	return res
}

// GetConfiguredNodes returns configured node IPs (defensive copy to prevent data races).
func (m *TalosManager) GetConfiguredNodes() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.nodes == nil {
		return nil
	}
	res := make([]string, len(m.nodes))
	copy(res, m.nodes)
	return res
}

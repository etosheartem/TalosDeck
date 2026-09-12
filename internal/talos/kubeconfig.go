package talos

import (
	"context"
	"errors"

	"github.com/siderolabs/talos/pkg/machinery/client"
)

func (m *TalosManager) GetKubernetesConfig(ctx context.Context, node string) ([]byte, error) {
	c := m.GetClient()
	if c == nil {
		return nil, errors.New("Talos client unavailable")
	}
	return c.Kubeconfig(client.WithNode(ctx, node))
}

package talos

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/client"
)

// StreamDmesg streams kernel dmesg messages from the specified node into the provided channel.
func (m *TalosManager) StreamDmesg(ctx context.Context, nodeIP string, logChan chan<- string, follow bool) error {
	nodeCtx := client.WithNode(ctx, nodeIP)

	stream, err := m.client.Dmesg(nodeCtx, follow, false)
	if err != nil {
		return fmt.Errorf("failed to initiate dmesg stream on %s: %w", nodeIP, err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			msg, err := stream.Recv()
			if err != nil {
				if err == io.EOF || ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("dmesg stream read error: %w", err)
			}

			payload := msg.GetBytes()
			if len(payload) == 0 {
				continue
			}

			// Some messages may contain multiple lines
			scanner := bufio.NewScanner(bytes.NewReader(payload))
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					continue
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case logChan <- line:
				}
			}
		}
	}
}

// StreamServiceLogs streams logs for a specific service container from the node into the channel.
func (m *TalosManager) StreamServiceLogs(ctx context.Context, nodeIP, serviceID string, logChan chan<- string, tailLines int32, follow bool) error {
	nodeCtx := client.WithNode(ctx, nodeIP)

	if tailLines <= 0 {
		tailLines = 100
	}

	stream, err := m.client.Logs(nodeCtx, "system", common.ContainerDriver_CONTAINERD, serviceID, follow, tailLines)
	if err != nil {
		return fmt.Errorf("failed to stream logs for service %s on %s: %w", serviceID, nodeIP, err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			msg, err := stream.Recv()
			if err != nil {
				if err == io.EOF || ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("service logs stream read error: %w", err)
			}

			payload := msg.GetBytes()
			if len(payload) == 0 {
				continue
			}

			scanner := bufio.NewScanner(bytes.NewReader(payload))
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					continue
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case logChan <- line:
				}
			}
		}
	}
}

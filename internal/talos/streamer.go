package talos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/siderolabs/talos/pkg/machinery/api/common"
	"github.com/siderolabs/talos/pkg/machinery/client"
)

// StreamDmesg streams kernel dmesg messages from the specified node into the provided channel.
func (m *TalosManager) StreamDmesg(ctx context.Context, nodeIP string, logChan chan<- string, follow bool) error {
	var cancel context.CancelFunc
	if !follow {
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}

	nodeCtx := client.WithNode(ctx, nodeIP)

	stream, err := m.client.Dmesg(nodeCtx, follow, false)
	if err != nil {
		return fmt.Errorf("failed to initiate dmesg stream on %s: %w", nodeIP, err)
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF || ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("dmesg stream read error: %w", err)
		}

		payload := msg.GetBytes()
		if len(payload) == 0 {
			continue
		}

		// Split lines without buffer-size limits (prevents ErrTooLong truncations)
		lines := bytes.Split(payload, []byte{'\n'})
		for _, lineBytes := range lines {
			lineBytes = bytes.TrimRight(lineBytes, "\r")
			if len(lineBytes) == 0 {
				continue
			}

			select {
			case <-ctx.Done():
				return nil
			case logChan <- string(lineBytes):
			}
		}
	}
}

// StreamServiceLogs streams logs for a specific service container from the node into the channel.
func (m *TalosManager) StreamServiceLogs(ctx context.Context, nodeIP, serviceID string, logChan chan<- string, tailLines int32, follow bool) error {
	var cancel context.CancelFunc
	if !follow {
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}

	nodeCtx := client.WithNode(ctx, nodeIP)

	if tailLines <= 0 {
		tailLines = 100
	}

	stream, err := m.client.Logs(nodeCtx, "system", common.ContainerDriver_CONTAINERD, serviceID, follow, tailLines)
	if err != nil {
		return fmt.Errorf("failed to stream logs for service %s on %s: %w", serviceID, nodeIP, err)
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF || ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("service logs stream read error: %w", err)
		}

		payload := msg.GetBytes()
		if len(payload) == 0 {
			continue
		}

		lines := bytes.Split(payload, []byte{'\n'})
		for _, lineBytes := range lines {
			lineBytes = bytes.TrimRight(lineBytes, "\r")
			if len(lineBytes) == 0 {
				continue
			}

			select {
			case <-ctx.Done():
				return nil
			case logChan <- string(lineBytes):
			}
		}
	}
}

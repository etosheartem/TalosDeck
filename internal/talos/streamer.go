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

// maxLogLineBytes caps the fragment held while waiting for a newline, so a
// stream that never emits one cannot grow the buffer without bound.
const maxLogLineBytes = 1 << 20

// streamLines pumps a Talos log stream into logChan. gRPC chunk boundaries are
// arbitrary: a single line may span several messages, so only complete lines are
// emitted and the trailing fragment carries over into the next chunk.
func streamLines(ctx context.Context, recv func() ([]byte, error), logChan chan<- string) error {
	var carry []byte

	emit := func(line []byte) bool {
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case logChan <- string(line):
			return true
		}
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		payload, err := recv()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
				// Flush whatever the node sent without a trailing newline.
				emit(carry)
				return nil
			}
			return err
		}

		if len(payload) == 0 {
			continue
		}

		carry = append(carry, payload...)

		for {
			idx := bytes.IndexByte(carry, '\n')
			if idx < 0 {
				break
			}
			if !emit(carry[:idx]) {
				return nil
			}
			carry = carry[idx+1:]
		}

		if len(carry) > maxLogLineBytes {
			if !emit(carry) {
				return nil
			}
			carry = carry[:0]
		}
	}
}

// StreamDmesg streams kernel dmesg messages from the specified node into the provided channel.
func (m *TalosManager) StreamDmesg(ctx context.Context, nodeIP string, logChan chan<- string, follow bool) error {
	talosClient := m.GetClient()
	if talosClient == nil {
		return errors.New("talos client is not initialized")
	}

	var cancel context.CancelFunc
	if !follow {
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}

	nodeCtx := client.WithNode(ctx, nodeIP)

	stream, err := talosClient.Dmesg(nodeCtx, follow, false)
	if err != nil {
		return fmt.Errorf("failed to initiate dmesg stream on %s: %w", nodeIP, err)
	}

	if err := streamLines(ctx, func() ([]byte, error) {
		msg, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return msg.GetBytes(), nil
	}, logChan); err != nil {
		return fmt.Errorf("dmesg stream read error: %w", err)
	}

	return nil
}

// StreamServiceLogs streams logs for a specific service container from the node into the channel.
func (m *TalosManager) StreamServiceLogs(ctx context.Context, nodeIP, serviceID string, logChan chan<- string, tailLines int32, follow bool) error {
	talosClient := m.GetClient()
	if talosClient == nil {
		return errors.New("talos client is not initialized")
	}

	var cancel context.CancelFunc
	if !follow {
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}

	nodeCtx := client.WithNode(ctx, nodeIP)

	if tailLines <= 0 {
		tailLines = 100
	}

	stream, err := talosClient.Logs(nodeCtx, "system", common.ContainerDriver_CONTAINERD, serviceID, follow, tailLines)
	if err != nil {
		return fmt.Errorf("failed to stream logs for service %s on %s: %w", serviceID, nodeIP, err)
	}

	if err := streamLines(ctx, func() ([]byte, error) {
		msg, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return msg.GetBytes(), nil
	}, logChan); err != nil {
		return fmt.Errorf("service logs stream read error: %w", err)
	}

	return nil
}

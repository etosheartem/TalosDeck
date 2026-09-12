package executionauthority

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

type SSHOptions struct {
	Host         string
	RemoteBinary string
	StateDir     string
}

type Client struct {
	mu         sync.Mutex
	input      io.WriteCloser
	output     io.ReadCloser
	scan       *bufio.Scanner
	stop       func()
	poisoned   bool
	InstanceID string
	Epoch      uint64
}

var safeRemote = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)
var safeHost = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@-]*$`)

// OpenSSH never reconnects. Any transport failure permanently poisons the handle.
// Host keys must already be trusted; neither passwords nor interactive prompts
// are accepted. Remote paths exclude shell metacharacters.
func OpenSSH(ctx context.Context, opts SSHOptions, instance string) (*Client, error) {
	if !safeHost.MatchString(opts.Host) || !validInstance(instance) || !filepath.IsAbs(opts.RemoteBinary) || !filepath.IsAbs(opts.StateDir) || !safeRemote.MatchString(opts.RemoteBinary) || !safeRemote.MatchString(opts.StateDir) {
		return nil, errors.New("invalid SSH authority configuration")
	}
	command := exec.Command("ssh", "-T", "-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-oConnectTimeout=5", "-oServerAliveInterval=5", "-oServerAliveCountMax=2", opts.Host, opts.RemoteBinary, "authority", "serve", "--state-dir", opts.StateDir)
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	// Stderr deliberately isn't returned: SSH output may include local metadata.
	if err = command.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, errors.New("cannot start authority SSH transport")
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	stop := func() { _ = command.Process.Kill(); <-done }
	c, err := openTransport(ctx, input, output, stop, instance)
	return c, err
}
func openTransport(ctx context.Context, input io.WriteCloser, output io.ReadCloser, stop func(), instance string) (*Client, error) {
	c := &Client{input: input, output: output, stop: stop, InstanceID: instance}
	c.scan = bufio.NewScanner(output)
	c.scan.Buffer(make([]byte, 1024), 4096)
	c.mu.Lock()
	defer c.mu.Unlock()
	reply, err := c.exchange(ctx, message{Op: "acquire", Instance: instance})
	if err != nil || reply.Op != "acquired" || reply.Instance != instance || reply.Epoch == 0 {
		c.poison()
		return nil, errors.New("execution authority acquisition failed")
	}
	c.Epoch = reply.Epoch
	return c, nil
}
func (c *Client) exchange(ctx context.Context, req message) (message, error) {
	if c.poisoned {
		return message{}, errors.New("authority lease lost")
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	type result struct {
		reply message
		err   error
	}
	done := make(chan result, 1)
	go func() {
		if err := json.NewEncoder(c.input).Encode(req); err != nil {
			done <- result{err: err}
			return
		}
		if !c.scan.Scan() {
			done <- result{err: errors.New("authority disconnected")}
			return
		}
		var reply message
		err := json.Unmarshal(c.scan.Bytes(), &reply)
		if reply.Error != "" {
			err = errors.New("authority rejected request")
		}
		done <- result{reply, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			c.poison()
		}
		return r.reply, r.err
	case <-check.Done():
		c.poison()
		<-done
		return message{}, errors.New("authority timed out")
	}
}
func (c *Client) poison() {
	if c.poisoned {
		return
	}
	c.poisoned = true
	_ = c.input.Close()
	_ = c.output.Close()
	c.stop()
}
func (c *Client) Validate(ctx context.Context, instance string, epoch uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.poisoned || instance != c.InstanceID || epoch != c.Epoch {
		return errors.New("execution authority unavailable or superseded")
	}
	reply, err := c.exchange(ctx, message{Op: "validate", Instance: instance, Epoch: epoch})
	if err != nil || reply.Op != "valid" || reply.Instance != instance || reply.Epoch != epoch {
		c.poison()
		return errors.New("execution authority unavailable or superseded")
	}
	return nil
}
func (c *Client) Close() error { c.mu.Lock(); defer c.mu.Unlock(); c.poison(); return nil }

package operations

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const TalosctlVersion = "v1.14.0"

type CommandRunner interface {
	Check(context.Context) error
	Run(context.Context, []string, func(string) error) error
}
type CLI struct{ Path string }

func (c CLI) executable() string {
	if c.Path != "" {
		return c.Path
	}
	return "talosctl"
}
func (c CLI) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, c.executable(), "version", "--client").CombinedOutput()
	if err != nil {
		return fmt.Errorf("talosctl %s is required on the server: %w", TalosctlVersion, err)
	}
	if !regexp.MustCompile(`(?m)Tag:\s+` + regexp.QuoteMeta(TalosctlVersion) + `\s`).Match(output) {
		return fmt.Errorf("talosctl version must be %s (matching the bundled SDK)", TalosctlVersion)
	}
	return nil
}

var sensitiveLine = regexp.MustCompile(`(?i)(authorization|bearer\s|password|private.?key|client-key-data|client-certificate-data|\btoken\b|\bsecret\b)`)

func safeLine(line string) string {
	if sensitiveLine.MatchString(line) {
		return "[redacted sensitive command output]"
	}
	return strings.Map(func(r rune) rune {
		if r < ' ' && r != '\t' {
			return ' '
		}
		return r
	}, line)
}

// Run never invokes a shell and does not pass application secrets to talosctl.
// Returning a journal write error terminates the child rather than continuing
// an unrecorded operation. The surrounding job must then be reviewed.
func (c CLI) Run(ctx context.Context, args []string, log func(string) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.executable(), args...)
	for _, key := range []string{"PATH", "HOME", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.WaitDelay = 3 * time.Second
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	var wg sync.WaitGroup
	var logErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		scan := bufio.NewScanner(reader)
		scan.Buffer(make([]byte, 4096), 1024*1024)
		for scan.Scan() {
			if err := log(safeLine(scan.Text())); err != nil {
				logErr = err
				cancel()
				break
			}
		}
		if err := scan.Err(); err != nil {
			logErr = err
			cancel()
		}
		reader.Close()
	}()
	runErr := cmd.Run()
	writer.Close()
	wg.Wait()
	if logErr != nil {
		return fmt.Errorf("command journal failed: %w", logErr)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if runErr != nil {
		return fmt.Errorf("talosctl failed: %w; inspect the execution log", runErr)
	}
	return nil
}

var ErrNoChanges = errors.New("all nodes already run the requested version")

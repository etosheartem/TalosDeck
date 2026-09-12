package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCLICheckBoundsInheritedOutputPipe(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "talosctl")
	pidFile := filepath.Join(dir, "child.pid")
	body := fmt.Sprintf("#!/bin/sh\nsleep 30 &\necho $! > %q\nwait\n", pidFile)
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		data, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := (CLI{Path: script}).Check(ctx); err == nil {
		t.Fatal("timed out command accepted")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("version check outlived deadline and WaitDelay: %v", elapsed)
	}
}
func TestCLICheckDoesNotInheritApplicationCredentials(t *testing.T) {
	t.Setenv("TALOSDECK_TEST_PASSWORD", "private-canary")
	script := filepath.Join(t.TempDir(), "talosctl")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ -n \"$TALOSDECK_TEST_PASSWORD\" ]; then exit 1; fi\nprintf 'Tag: v1.14.0\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := (CLI{Path: script}).Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestVersionOutputCaptureIsBounded(t *testing.T) {
	b := &limitedCommandOutput{}
	input := []byte(strings.Repeat("x", 2*1024*1024))
	n, err := b.Write(input)
	if err != nil || n != len(input) || b.Len() != 64*1024 {
		t.Fatal("output cap or writer contract violated")
	}
}

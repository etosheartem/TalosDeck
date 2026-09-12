// Package executionauthority supplies an exclusive execution lease on a host
// independent of the management VM. It does not revoke provider requests that
// were already in flight when the lease was lost.
package executionauthority

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type message struct {
	Op       string `json:"op"`
	Instance string `json:"instance"`
	Epoch    uint64 `json:"epoch"`
	Error    string `json:"error,omitempty"`
}

// Serve holds a local POSIX flock for the full transport session. StateDir must
// be on the independent authority host's local filesystem, never in a backup or
// on NFS. Lock takeover is deliberately unsupported.
func Serve(ctx context.Context, dir string, input io.Reader, output io.Writer) error {
	if !filepath.IsAbs(dir) {
		return errors.New("authority state directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	_, lockStatErr := os.Lstat(filepath.Join(dir, "execution.lock"))
	hadLock := lockStatErr == nil
	if lockStatErr != nil && !os.IsNotExist(lockStatErr) {
		return lockStatErr
	}
	lock, err := os.OpenFile(filepath.Join(dir, "execution.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), 4096)
	encoder := json.NewEncoder(output)
	if !scanner.Scan() {
		return errors.New("authority handshake missing")
	}
	var hello message
	if json.Unmarshal(scanner.Bytes(), &hello) != nil || hello.Op != "acquire" || !validInstance(hello.Instance) {
		return errors.New("invalid authority handshake")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = encoder.Encode(message{Error: "authority already leased"})
		return errors.New("authority already leased")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	epochPath := filepath.Join(dir, "epoch")
	raw, err := os.ReadFile(epochPath)
	var epoch uint64
	if err == nil {
		epoch, err = strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	} else if os.IsNotExist(err) && !hadLock {
		err = nil
	}
	if err != nil || epoch == ^uint64(0) {
		return errors.New("authority epoch is invalid")
	}
	epoch++
	tmp, err := os.CreateTemp(dir, ".epoch-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, err = fmt.Fprintf(tmp, "%d\n", epoch)
	if err == nil {
		err = tmp.Sync()
	}
	ce := tmp.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, epochPath); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return err
	}
	if err = encoder.Encode(message{Op: "acquired", Instance: hello.Instance, Epoch: epoch}); err != nil {
		return err
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var req message
		if json.Unmarshal(scanner.Bytes(), &req) != nil || req.Op != "validate" || req.Instance != hello.Instance || req.Epoch != epoch {
			return errors.New("authority identity mismatch")
		}
		current, err := os.ReadFile(epochPath)
		if err != nil || strings.TrimSpace(string(current)) != strconv.FormatUint(epoch, 10) {
			return errors.New("authority epoch changed")
		}
		// Verify the locked inode has not been replaced by another administrator.
		held, err := lock.Stat()
		if err != nil {
			return err
		}
		path, err := os.Stat(filepath.Join(dir, "execution.lock"))
		if err != nil || !os.SameFile(held, path) {
			return errors.New("authority lock identity changed")
		}
		if err = encoder.Encode(message{Op: "valid", Instance: hello.Instance, Epoch: epoch}); err != nil {
			return err
		}
	}
	return scanner.Err()
}
func validInstance(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

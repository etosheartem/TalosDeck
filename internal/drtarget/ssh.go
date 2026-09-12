package drtarget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var sshHostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*(?:@[A-Za-z0-9][A-Za-z0-9_.-]*)?$`)
var sshDirPattern = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
var sshObjectPattern = regexp.MustCompile(`^[a-f0-9-]{36}\.tdr$`)

func validateSSH(host, directory string) error {
	if !sshHostPattern.MatchString(host) || !sshDirPattern.MatchString(directory) || path.Clean(directory) != directory || directory == "/" {
		return errors.New("SSH target requires a trusted host alias and clean absolute directory using letters, digits, slash, dot, underscore or hyphen")
	}
	for _, part := range strings.Split(directory, "/") {
		if part == ".." || part == "." {
			return errors.New("invalid SSH target directory")
		}
	}
	return nil
}
func sshCommand(ctx context.Context, host, command string, stdin io.Reader, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "--", host, command)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	// Do not surface server-controlled output or sensitive SSH configuration details.
	if err := cmd.Run(); err != nil {
		return errors.New("SSH recovery transfer failed or was interrupted")
	}
	return nil
}

// SSHUpload publishes a unique archive on a preconfigured, independently durable
// SSH host. It does not accept host keys automatically. Directory ownership and
// separation from the management host are deployment prerequisites.
// On ambiguous failure Receipt.Object identifies a possible orphan; never retry
// by overwriting that identity. A successful receipt requires full readback.
func SSHUpload(ctx context.Context, host, directory, archivePath string) (Receipt, error) {
	if err := validateSSH(host, directory); err != nil {
		return Receipt{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	f, err := os.Open(archivePath)
	if err != nil {
		return Receipt{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Receipt{}, err
	}
	if n <= 0 {
		return Receipt{}, errors.New("empty recovery archive")
	}
	r := Receipt{Object: uuid.NewString() + ".tdr", Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return r, err
	}
	target := directory + "/" + r.Object
	temporary := target + ".upload"
	// noclobber reserves the temporary identity; hard-link publication is atomic
	// and refuses an existing final name. Failed uploads may retain their own temp.
	command := fmt.Sprintf("umask 077; mkdir -p '%s' && (set -C; cat > '%s') && ln '%s' '%s' && rm '%s' && sync -f '%s'", directory, temporary, temporary, target, temporary, target)
	if err = sshCommand(ctx, host, command, f, io.Discard); err != nil {
		return r, errors.New("SSH upload outcome unknown; retain archive and inspect receipt object and its .upload sibling")
	}
	h.Reset()
	counter := &sshBoundedWriter{writer: h, remaining: n}
	if err = sshCommand(ctx, host, "cat '"+target+"'", nil, counter); err != nil || counter.remaining != 0 || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return r, errors.New("SSH uploaded archive readback verification failed; retain local archive")
	}
	return r, nil
}

type sshBoundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *sshBoundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("SSH object exceeds expected size")
	}
	n, e := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, e
}

// SSHDownload writes only a new local path and verifies the independent receipt.
func SSHDownload(ctx context.Context, host, directory string, r Receipt, destination string) error {
	if err := validateSSH(host, directory); err != nil {
		return err
	}
	if !sshObjectPattern.MatchString(r.Object) || r.Size <= 0 || len(r.SHA256) != 64 {
		return errors.New("invalid SSH recovery receipt")
	}
	if _, err := uuid.Parse(strings.TrimSuffix(r.Object, ".tdr")); err != nil {
		return errors.New("invalid SSH object identity")
	}
	if _, err := hex.DecodeString(r.SHA256); err != nil {
		return errors.New("invalid SSH recovery checksum")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(destination)
		}
	}()
	h := sha256.New()
	counter := &sshBoundedWriter{writer: io.MultiWriter(f, h), remaining: r.Size}
	if err = sshCommand(ctx, host, "cat '"+directory+"/"+r.Object+"'", nil, counter); err != nil {
		return err
	}
	if counter.remaining != 0 || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return errors.New("SSH recovery archive checksum or size mismatch")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

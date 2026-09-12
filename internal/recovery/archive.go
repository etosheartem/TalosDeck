// Package recovery implements offline, key-separated management-plane archives.
package recovery

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
	"talosdeck/internal/clusters"
)

const SafeModeFile = "recovery-required.json"
const maxFiles = 100000
const maxBytes int64 = 100 << 30

type Options struct{ DataDir, KeyPath, ArchivePath, ApplicationVersion string }
type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	FormatVersion      int       `json:"formatVersion"`
	SchemaVersion      int       `json:"schemaVersion"`
	ApplicationVersion string    `json:"applicationVersion"`
	CreatedAt          time.Time `json:"createdAt"`
	KeySHA256          string    `json:"keySHA256"`
	Entries            []Entry   `json:"entries"`
}

func digestFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func validPath(p string) bool {
	return p != "" && p != "." && !strings.Contains(p, "\\") && !filepath.IsAbs(p) && filepath.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}

// Create requires the server to be stopped. ArchivePath must be outside DataDir.
// Its caller must upload the result to an independently durable off-host target.
func Create(ctx context.Context, o Options) (m Manifest, err error) {
	data, err := filepath.Abs(o.DataDir)
	if err != nil {
		return m, err
	}
	out, err := filepath.Abs(o.ArchivePath)
	if err != nil {
		return m, err
	}
	if out == data || strings.HasPrefix(out, data+string(os.PathSeparator)) {
		return m, errors.New("archive must be outside data directory")
	}
	keyInfo, err := os.Stat(o.KeyPath)
	if err != nil {
		return m, err
	}
	if _, err = os.Stat(filepath.Join(data, "talosdeck.db")); err != nil {
		return m, err
	}
	lock, err := os.OpenFile(filepath.Join(data, "talosdeck.db.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return m, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return m, errors.New("stop TalosDeck before creating an offline recovery backup")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	// A hot journal means the offline filesystem copy is not yet a clean snapshot.
	for _, suffix := range []string{"-wal", "-journal"} {
		info, e := os.Stat(filepath.Join(data, "talosdeck.db") + suffix)
		if e != nil && !os.IsNotExist(e) {
			return m, e
		}
		if e == nil && info.Size() > 0 {
			return m, errors.New("database has an uncheckpointed journal; complete database recovery before backup")
		}
	}
	stage, err := os.MkdirTemp("", "talosdeck-backup-*")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(stage)
	var total int64
	err = filepath.WalkDir(data, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if path == data {
			return nil
		}
		rel, e := filepath.Rel(data, path)
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink not allowed in data directory: %s", rel)
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(stage, rel), 0700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular durable file: %s", rel)
		}
		if os.SameFile(info, keyInfo) || d.Name() == "master.key" || strings.HasSuffix(rel, ".lock") || rel == SafeModeFile {
			return nil
		}
		total += info.Size()
		if total > maxBytes || len(m.Entries) >= maxFiles {
			return errors.New("archive limits exceeded")
		}
		if e = copyFile(path, filepath.Join(stage, rel)); e != nil {
			return e
		}
		sum, e := digestFile(filepath.Join(stage, rel))
		if e != nil {
			return e
		}
		m.Entries = append(m.Entries, Entry{rel, info.Size(), sum})
		return nil
	})
	if err != nil {
		return m, err
	}
	m.FormatVersion = 1
	m.ApplicationVersion = o.ApplicationVersion
	m.CreatedAt = time.Now().UTC()
	m.KeySHA256, err = digestFile(o.KeyPath)
	if err != nil {
		return m, err
	}
	db, e := sql.Open("sqlite", filepath.Join(stage, "talosdeck.db"))
	if e != nil {
		return m, e
	}
	err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&m.SchemaVersion)
	db.Close()
	if err != nil {
		return m, err
	}
	// Validate a separate copy: opening the store may migrate schema or mark revisions interrupted.
	check, err := os.MkdirTemp("", "talosdeck-backup-check-*")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(check)
	for _, entry := range m.Entries {
		if err = copyFile(filepath.Join(stage, entry.Path), filepath.Join(check, entry.Path)); err != nil {
			return m, err
		}
	}
	if err = validate(ctx, check, o.KeyPath); err != nil {
		return m, err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return m, err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(out)
		}
	}()
	encrypted, err := archiveWriter(f, o.KeyPath)
	if err != nil {
		return m, err
	}
	tw := tar.NewWriter(encrypted)
	b, _ := json.Marshal(m)
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(b))}); err != nil {
		return m, err
	}
	if _, err = tw.Write(b); err != nil {
		return m, err
	}
	for _, entry := range m.Entries {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		if err = tw.WriteHeader(&tar.Header{Name: "data/" + entry.Path, Mode: 0600, Size: entry.Size}); err != nil {
			return m, err
		}
		src, e := os.Open(filepath.Join(stage, entry.Path))
		if e != nil {
			return m, e
		}
		_, err = io.Copy(tw, src)
		src.Close()
		if err != nil {
			return m, err
		}
	}
	if err = tw.Close(); err != nil {
		return m, err
	}
	if err = encrypted.Close(); err != nil {
		return m, err
	}
	if err = f.Sync(); err != nil {
		return m, err
	}
	if err = f.Close(); err != nil {
		return m, err
	}
	ok = true
	return m, nil
}
func copyFile(src, dst string) error {
	if e := os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
		return e
	}
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	if e == nil {
		e = out.Sync()
	}
	ce := out.Close()
	if e != nil {
		return e
	}
	return ce
}
func validate(ctx context.Context, dir, key string) error {
	db, e := sql.Open("sqlite", filepath.Join(dir, "talosdeck.db"))
	if e != nil {
		return e
	}
	var result string
	e = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result)
	db.Close()
	if e != nil {
		return e
	}
	if result != "ok" {
		return errors.New("database integrity check failed")
	}
	s, e := clusters.Open(filepath.Join(dir, "talosdeck.db"), key)
	if e != nil {
		return e
	}
	return s.Close()
}

// Restore validates in isolation and publishes only to a previously nonexistent directory.
// It never starts jobs or connects to infrastructure. The key remains at KeyPath.
func Restore(ctx context.Context, archivePath, destDir, keyPath string) (m Manifest, err error) {
	if _, e := os.Lstat(destDir); !os.IsNotExist(e) {
		return m, errors.New("restore destination must not exist")
	}
	parent := filepath.Dir(destDir)
	stage, e := os.MkdirTemp(parent, ".talosdeck-restore-*")
	if e != nil {
		return m, e
	}
	defer os.RemoveAll(stage)
	m, err = extract(ctx, archivePath, stage, keyPath)
	if err != nil {
		return m, err
	}
	if err = validate(ctx, stage, keyPath); err != nil {
		return m, err
	}
	marker, _ := json.Marshal(map[string]any{"restoredAt": time.Now().UTC(), "backupCreatedAt": m.CreatedAt, "requiresReview": true, "reason": "management plane restored; destructive jobs and schedules must remain disabled"})
	markerFile, err := os.OpenFile(filepath.Join(stage, SafeModeFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return m, err
	}
	if _, err = markerFile.Write(marker); err == nil {
		err = markerFile.Sync()
	}
	closeErr := markerFile.Close()
	if err != nil {
		return m, err
	}
	if closeErr != nil {
		return m, closeErr
	}
	stageDir, err := os.Open(stage)
	if err != nil {
		return m, err
	}
	err = stageDir.Sync()
	stageDir.Close()
	if err != nil {
		return m, err
	}
	// Atomically publish without replacing a concurrently created destination.
	if err = unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destDir, unix.RENAME_NOREPLACE); err != nil {
		return m, err
	}
	d, e := os.Open(parent)
	if e != nil {
		return m, e
	}
	defer d.Close()
	err = d.Sync()
	return m, err
}
func extract(ctx context.Context, path, dir, key string) (m Manifest, err error) {
	f, e := os.Open(path)
	if e != nil {
		return m, e
	}
	defer f.Close()
	decrypted, e := archiveReader(f, key)
	if e != nil {
		return m, e
	}
	tr := tar.NewReader(decrypted)
	h, e := tr.Next()
	if e != nil {
		return m, e
	}
	if h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size > 16<<20 {
		return m, errors.New("invalid recovery manifest")
	}
	if e = json.NewDecoder(io.LimitReader(tr, 16<<20)).Decode(&m); e != nil {
		return m, e
	}
	if m.FormatVersion != 1 || m.SchemaVersion < 1 || m.SchemaVersion > 2 || len(m.Entries) > maxFiles || m.CreatedAt.IsZero() {
		return m, errors.New("unsupported backup format or schema")
	}
	expected := map[string]Entry{}
	var total int64
	for _, entry := range m.Entries {
		if !validPath(entry.Path) || entry.Path == SafeModeFile || filepath.Base(entry.Path) == "master.key" || entry.Size < 0 {
			return m, errors.New("unsafe archive entry")
		}
		if _, exists := expected[entry.Path]; exists {
			return m, errors.New("duplicate manifest entry")
		}
		total += entry.Size
		if total > maxBytes {
			return m, errors.New("archive size limit exceeded")
		}
		expected[entry.Path] = entry
	}
	if _, exists := expected["talosdeck.db"]; !exists {
		return m, errors.New("database missing from backup")
	}
	for {
		if e = ctx.Err(); e != nil {
			return m, e
		}
		h, e = tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return m, e
		}
		if !strings.HasPrefix(h.Name, "data/") || h.Typeflag != tar.TypeReg {
			return m, errors.New("unexpected archive entry")
		}
		rel := strings.TrimPrefix(h.Name, "data/")
		entry, ok := expected[rel]
		if !ok || h.Size != entry.Size {
			return m, errors.New("archive entry does not match manifest")
		}
		delete(expected, rel)
		dst := filepath.Join(dir, rel)
		if e = os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
			return m, e
		}
		out, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return m, e
		}
		hash := sha256.New()
		_, e = io.Copy(io.MultiWriter(out, hash), tr)
		if e == nil {
			e = out.Sync()
		}
		ce := out.Close()
		if e != nil {
			return m, e
		}
		if ce != nil {
			return m, ce
		}
		if hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return m, errors.New("backup checksum mismatch")
		}
	}
	if _, e = io.Copy(io.Discard, decrypted); e != nil {
		return m, e
	}
	if len(expected) != 0 {
		return m, errors.New("incomplete recovery archive")
	}
	return m, nil
}

// Drill validates and migrates an ephemeral copy without updating the backup timestamp.
func Drill(ctx context.Context, archivePath, keyPath string) (Manifest, error) {
	d, e := os.MkdirTemp("", "talosdeck-drill-*")
	if e != nil {
		return Manifest{}, e
	}
	defer os.RemoveAll(d)
	return Restore(ctx, archivePath, filepath.Join(d, "data"), keyPath)
}

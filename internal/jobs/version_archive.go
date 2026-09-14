package jobs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
)

// preserveIncompatibleJournal durably retains the original opaque document
// before the current binary rewrites its known fields. Linking the synced temp
// file installs it atomically without replacing evidence from a prior restart.
func preserveIncompatibleJournal(dir, id string, data []byte) error {
	archiveDir := filepath.Join(dir, "incompatible-originals")
	if err := os.MkdirAll(archiveDir, 0700); err != nil {
		return err
	}
	target := filepath.Join(archiveDir, id+".json")
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return errors.New("unsafe incompatible journal archive")
		}
		return syncArchiveDirectories(archiveDir, dir) // Preserve the first input across restarts.
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(archiveDir, ".original-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(name, target); err != nil {
		// Normally impossible under the journal lock; never accept a raced replacement.
		existing, readErr := os.ReadFile(target)
		if !os.IsExist(err) || readErr != nil || !bytes.Equal(existing, data) {
			return err
		}
	}
	return syncArchiveDirectories(archiveDir, dir)
}

func syncArchiveDirectories(paths ...string) error {
	for _, path := range paths {
		d, err := os.Open(path)
		if err != nil {
			return err
		}
		err = d.Sync()
		closeErr := d.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

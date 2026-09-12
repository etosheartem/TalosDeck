package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"talosdeck/internal/executionauthority"
)

// Once enabled, authority is durable policy: omitting startup flags must not
// silently return an executor to unrestricted legacy mode after a restart.
func executionPolicy(dataDir string, requested executionauthority.SSHOptions) (executionauthority.SSHOptions, error) {
	path := filepath.Join(dataDir, "execution-authority.json")
	raw, err := os.ReadFile(path)
	if err == nil {
		var saved executionauthority.SSHOptions
		if json.Unmarshal(raw, &saved) != nil || saved.Host == "" || saved.RemoteBinary == "" || saved.StateDir == "" {
			return saved, errors.New("invalid persisted execution authority policy")
		}
		if requested.Host != "" && (requested.Host != saved.Host || requested.RemoteBinary != saved.RemoteBinary || requested.StateDir != saved.StateDir) {
			return saved, errors.New("execution authority policy differs from configured authority; offline migration review required")
		}
		return saved, nil
	}
	if !os.IsNotExist(err) {
		return requested, err
	}
	if requested.Host == "" {
		return requested, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return requested, err
	}
	err = json.NewEncoder(f).Encode(requested)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return requested, err
	}
	if ce != nil {
		return requested, ce
	}
	d, err := os.Open(dataDir)
	if err != nil {
		return requested, err
	}
	defer d.Close()
	return requested, d.Sync()
}

func recordExecutionEpoch(dataDir string, policy executionauthority.SSHOptions, epoch uint64) error {
	policy.ExpectedEpoch = epoch
	f, err := os.CreateTemp(dataDir, ".authority-policy-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	err = json.NewEncoder(f).Encode(policy)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = os.Rename(name, filepath.Join(dataDir, "execution-authority.json")); err != nil {
		return err
	}
	d, err := os.Open(dataDir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

package main

import (
	"talosdeck/internal/executionauthority"
	"testing"
)

func TestPersistedAuthorityCannotBeBypassedByOmittedFlags(t *testing.T) {
	dir := t.TempDir()
	want := executionauthority.SSHOptions{Host: "authority", RemoteBinary: "/bin/talosdeck", StateDir: "/state"}
	if _, err := executionPolicy(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := executionPolicy(dir, executionauthority.SSHOptions{})
	if err != nil || got != want {
		t.Fatal(got, err)
	}
	other := want
	other.Host = "other-authority"
	if _, err := executionPolicy(dir, other); err == nil {
		t.Fatal("silently changed authority")
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"talosdeck/internal/executionauthority"
	"talosdeck/internal/recovery"
)

func recoveryActivateCommand(args []string) error {
	fs := flag.NewFlagSet("recovery activate", flag.ContinueOnError)
	data := fs.String("data", "", "restored data directory; server must be stopped")
	key := fs.String("key", "", "independently supplied master keyring")
	actor := fs.String("actor", "", "operator performing recovery review")
	fencePath := fs.String("fencing-evidence", "", "file containing bounded old-management fencing attestation")
	reviewsPath := fs.String("reviews", "", "JSON array of explicit per-job review reasons")
	confirmed := fs.Bool("confirm-old-management-fenced", false, "attest old management instance cannot execute infrastructure operations")
	host := fs.String("authority-host", "", "independent SSH authority host (or use persisted policy)")
	binary := fs.String("authority-binary", "/usr/local/bin/talosdeck", "independent authority helper binary")
	state := fs.String("authority-state", "/var/lib/talosdeck-authority", "independent authority state directory")
	expected := fs.Uint64("expected-epoch", 0, "explicit reviewed expected epoch; never steals a live lease")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *data == "" || *key == "" || *actor == "" || !*confirmed || *fencePath == "" || *reviewsPath == "" {
		return errors.New("data, key, actor, fencing-evidence, reviews and confirm-old-management-fenced required")
	}
	if os.Getenv("TALOSDECK_JOBS_DIR") != "" {
		return errors.New("external legacy journals must be consolidated before activation")
	}
	if _, err := os.Stat(filepath.Join(*data, recovery.SafeModeFile)); err != nil {
		return errors.New("restored recovery marker required")
	}
	fence, err := os.ReadFile(*fencePath)
	if err != nil {
		return err
	}
	if len(fence) == 0 || len(fence) > 4096 {
		return errors.New("fencing attestation must be 1–4096 bytes and contain no credentials")
	}
	f, err := os.Open(*reviewsPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var reviews []recovery.JobReview
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&reviews); err != nil {
		return errors.New("invalid review list")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("unexpected trailing review data")
	}
	if len(reviews) > 10000 {
		return errors.New("too many review entries")
	}
	opts, err := executionPolicy(*data, executionauthority.SSHOptions{Host: *host, RemoteBinary: *binary, StateDir: *state})
	if err != nil {
		return err
	}
	if opts.Host == "" {
		return errors.New("independent execution authority required for recovery activation")
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "expected-epoch" {
			opts.ExpectedEpoch = *expected
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	instance := uuid.NewString()
	client, err := executionauthority.OpenSSH(ctx, opts, instance)
	if err != nil {
		return err
	}
	defer client.Close()
	receipt, err := recovery.Activate(ctx, recovery.ActivationOptions{DataDir: *data, KeyPath: *key, Actor: *actor, FencingEvidence: strings.TrimSpace(string(fence)), ConfirmOldManagementFenced: *confirmed, Reviews: reviews, Authority: client, InstanceID: instance, Epoch: client.Epoch, AuthorityIdentity: executionauthority.Identity(opts), PersistAuthorityEpoch: func() error { return recordExecutionEpoch(*data, opts, client.Epoch) }})
	if err != nil {
		return fmt.Errorf("activation failed closed after acquiring epoch %d; inspect the review failure before retrying: %w", client.Epoch, err)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"activatedAt": receipt.ActivatedAt, "epoch": receipt.Epoch, "authorityIdentity": receipt.AuthorityIdentity, "reviewDigest": receipt.ReviewDigest, "reviewedJobs": len(receipt.Reviews), "automationPaused": true})
}

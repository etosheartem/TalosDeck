package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"talosdeck/internal/drtarget"
	"talosdeck/internal/recovery"
)

// targetLocator identifies the archive without exposing target credentials.
func targetLocator(config drtarget.Config, object string) string {
	if object == "" {
		return ""
	}
	if config.Type == "ssh" {
		return "ssh://" + config.Host + "/" + strings.TrimPrefix(config.Directory, "/") + "/" + object
	}
	return "s3://" + config.Bucket + "/" + object
}

// recordObservation must never turn a completed recovery operation into a failure.
// A history directory that cannot be written is reported and the command result stands.
func recordObservation(dir string, r recovery.Record) {
	if err := recovery.Append(dir, r); err != nil {
		fmt.Fprintln(os.Stderr, "recovery history not recorded:", err)
	}
}

func recoveryCommand(args []string) error {
	if len(args) > 0 && args[0] == "activate" {
		return recoveryActivateCommand(args[1:])
	}
	if len(args) == 0 {
		return errors.New("usage: talosdeck recovery backup|restore|drill [flags]")
	}
	fs := flag.NewFlagSet("recovery "+args[0], flag.ContinueOnError)
	data := fs.String("data", "", "source data directory (backup) or NEW destination (restore)")
	key := fs.String("key", "", "independently supplied master keyring")
	target := fs.String("target", "", "independent S3 (HTTPS) or SSH target JSON file")
	receiptPath := fs.String("receipt", "", "receipt output (backup) or input (restore/drill)")
	historyDir := fs.String("history", "", "append the observation to this recovery history directory (read by the console)")
	completeData := fs.Bool("confirm-complete-data-directory", false, "confirm all durable journals/backups/config artifacts are under DATA; external deployment secrets are retained independently")
	timeout := fs.Duration("timeout", 30*time.Minute, "operation deadline")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *key == "" || *target == "" || *receiptPath == "" {
		return errors.New("key, target and receipt are required; backup/restore also require data")
	}
	if args[0] != "backup" && args[0] != "restore" && args[0] != "drill" {
		return errors.New("unknown recovery command")
	}
	if args[0] != "drill" && *data == "" {
		return errors.New("data directory required")
	}
	// Existing external legacy journals require explicit migration into DATA first.
	if args[0] == "backup" && (!*completeData || os.Getenv("TALOSDECK_JOBS_DIR") != "") {
		return errors.New("backup requires complete-data-directory confirmation and consolidation of external legacy journals/backups")
	}
	var config drtarget.Config
	raw, err := os.ReadFile(*target)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		return errors.New("invalid recovery target configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	temp, err := os.MkdirTemp("", "talosdeck-recovery-transfer-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	archive := filepath.Join(temp, "archive.tdr")
	if args[0] == "backup" {
		out, err := os.OpenFile(*receiptPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		complete := false
		defer func() {
			out.Close()
			if !complete {
				os.Remove(*receiptPath)
			}
		}()
		version := "development"
		if info, ok := debug.ReadBuildInfo(); ok {
			dirty := false
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					version = setting.Value
				}
				if setting.Key == "vcs.modified" && setting.Value == "true" {
					dirty = true
				}
			}
			if dirty {
				version += "+dirty"
			}
		}
		manifest, err := recovery.Create(ctx, recovery.Options{DataDir: *data, KeyPath: *key, ArchivePath: archive, ApplicationVersion: version})
		if err != nil {
			recordObservation(*historyDir, recovery.Record{Kind: "backup", Outcome: "failed", Error: err.Error()})
			return err
		}
		// Create validated integrity, decryption and schema on a separate copy
		// before any transfer; the archive is not yet off-host at this point.
		validatedAt := time.Now().UTC()
		var receipt drtarget.Receipt
		if config.Type == "ssh" {
			receipt, err = drtarget.SSHUpload(ctx, config.Host, config.Directory, archive)
		} else if config.Type == "s3" || config.Type == "" {
			receipt, err = drtarget.Upload(ctx, config, archive)
		} else {
			return errors.New("unsupported recovery target type")
		}
		if err != nil {
			// An upload that left a locator behind has an unproven outcome: the
			// object may exist and may be complete. Never record that as failed.
			outcome := "failed"
			if receipt.Object != "" {
				outcome = "unknown"
			}
			recordObservation(*historyDir, recovery.Record{Kind: "backup", Outcome: outcome, BackupCreatedAt: manifest.CreatedAt,
				DecryptVerifiedAt: &validatedAt, SchemaVerifiedAt: &validatedAt, SchemaVersion: manifest.SchemaVersion,
				ApplicationVersion: manifest.ApplicationVersion, Target: targetLocator(config, receipt.Object), Error: err.Error()})
			if receipt.Object != "" {
				if writeErr := json.NewEncoder(out).Encode(struct {
					Receipt  drtarget.Receipt `json:"receipt"`
					Verified bool             `json:"verified"`
					Outcome  string           `json:"outcome"`
				}{receipt, false, "unknown"}); writeErr == nil {
					if out.Sync() == nil {
						complete = true
						if parent, openErr := os.Open(filepath.Dir(*receiptPath)); openErr == nil {
							// Preserve the ambiguous-upload locator even when directory
							// durability cannot be confirmed; never erase that evidence.
							_ = parent.Sync()
							_ = parent.Close()
						}
					}
				}
			}
			return err
		}
		uploadedAt := time.Now().UTC()
		record := struct {
			Receipt    drtarget.Receipt  `json:"receipt"`
			Manifest   recovery.Manifest `json:"manifest"`
			UploadedAt time.Time         `json:"uploadedAt"`
		}{receipt, manifest, uploadedAt}
		if err = json.NewEncoder(out).Encode(record); err != nil {
			return err
		}
		if err = out.Sync(); err != nil {
			return err
		}
		parent, err := os.Open(filepath.Dir(*receiptPath))
		if err != nil {
			return err
		}
		err = parent.Sync()
		parent.Close()
		if err != nil {
			return err
		}
		complete = true
		// The transfer was read back from the target, so the checksum is proven
		// there; decryption and schema were proven locally before the upload.
		recordObservation(*historyDir, recovery.Record{Kind: "backup", Outcome: "succeeded", BackupCreatedAt: manifest.CreatedAt,
			UploadedAt: &uploadedAt, ChecksumVerifiedAt: &uploadedAt, DecryptVerifiedAt: &validatedAt, SchemaVerifiedAt: &validatedAt,
			SchemaVersion: manifest.SchemaVersion, ApplicationVersion: manifest.ApplicationVersion,
			Target: targetLocator(config, receipt.Object), SizeBytes: receipt.Size})
		fmt.Println("Off-host target upload read-back verified; retain receipt and master key separately. Target failure-domain independence must be verified by the operator.")
		return nil
	}
	var record struct {
		Receipt drtarget.Receipt `json:"receipt"`
	}
	raw, err = os.ReadFile(*receiptPath)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &record); err != nil {
		return errors.New("invalid recovery receipt")
	}
	if config.Type == "ssh" {
		err = drtarget.SSHDownload(ctx, config.Host, config.Directory, record.Receipt, archive)
	} else if config.Type == "s3" || config.Type == "" {
		err = drtarget.Download(ctx, config, record.Receipt, archive)
	} else {
		return errors.New("unsupported recovery target type")
	}
	locator := targetLocator(config, record.Receipt.Object)
	if err != nil {
		recordObservation(*historyDir, recovery.Record{Kind: args[0], Outcome: "failed", Target: locator, Error: err.Error()})
		return err
	}
	started := time.Now()
	var manifest recovery.Manifest
	if args[0] == "restore" {
		manifest, err = recovery.Restore(ctx, archive, *data, *key)
	} else {
		manifest, err = recovery.Drill(ctx, archive, *key)
	}
	if err != nil {
		recordObservation(*historyDir, recovery.Record{Kind: args[0], Outcome: "failed", BackupCreatedAt: manifest.CreatedAt,
			Target: locator, DurationSeconds: time.Since(started).Seconds(), Error: err.Error()})
		return err
	}
	// Every entry checksum, the archive key and the schema were proven on this copy.
	// The verification copy is discarded (drill) or published as a new data directory
	// (restore); neither becomes a backup, and neither changes BackupCreatedAt.
	checkedAt := time.Now().UTC()
	observation := recovery.Record{Kind: args[0], Outcome: "succeeded", BackupCreatedAt: manifest.CreatedAt,
		ChecksumVerifiedAt: &checkedAt, DecryptVerifiedAt: &checkedAt, SchemaVerifiedAt: &checkedAt,
		SchemaVersion: manifest.SchemaVersion, ApplicationVersion: manifest.ApplicationVersion,
		Target: locator, SizeBytes: record.Receipt.Size, DurationSeconds: time.Since(started).Seconds()}
	if args[0] == "drill" {
		observation.RestoreTestedAt = &checkedAt
	}
	recordObservation(*historyDir, observation)
	return json.NewEncoder(os.Stdout).Encode(struct {
		Operation         string            `json:"operation"`
		Manifest          recovery.Manifest `json:"manifest"`
		ValidationSeconds float64           `json:"validationSeconds"`
		CheckedAt         time.Time         `json:"checkedAt"`
	}{args[0], manifest, time.Since(started).Seconds(), time.Now().UTC()})
}

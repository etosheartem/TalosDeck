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
	"time"

	"talosdeck/internal/drtarget"
	"talosdeck/internal/recovery"
)

func recoveryCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: talosdeck recovery backup|restore|drill [flags]")
	}
	fs := flag.NewFlagSet("recovery "+args[0], flag.ContinueOnError)
	data := fs.String("data", "", "source data directory (backup) or NEW destination (restore)")
	key := fs.String("key", "", "independently supplied master keyring")
	target := fs.String("target", "", "independent S3 (HTTPS) or SSH target JSON file")
	receiptPath := fs.String("receipt", "", "receipt output (backup) or input (restore/drill)")
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
	if os.Getenv("TALOSDECK_JOBS_DIR") != "" {
		return errors.New("external legacy jobs directory configured: consolidate durable state before recovery backup")
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
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					version = setting.Value
				}
			}
		}
		manifest, err := recovery.Create(ctx, recovery.Options{DataDir: *data, KeyPath: *key, ArchivePath: archive, ApplicationVersion: version})
		if err != nil {
			return err
		}
		var receipt drtarget.Receipt
		if config.Type == "ssh" {
			receipt, err = drtarget.SSHUpload(ctx, config.Host, config.Directory, archive)
		} else if config.Type == "s3" || config.Type == "" {
			receipt, err = drtarget.Upload(ctx, config, archive)
		} else {
			return errors.New("unsupported recovery target type")
		}
		if err != nil {
			if receipt.Object != "" {
				if writeErr := json.NewEncoder(out).Encode(struct {
					Receipt  drtarget.Receipt `json:"receipt"`
					Verified bool             `json:"verified"`
					Outcome  string           `json:"outcome"`
				}{receipt, false, "unknown"}); writeErr == nil {
					if out.Sync() == nil {
						complete = true
					}
				}
			}
			return err
		}
		record := struct {
			Receipt    drtarget.Receipt  `json:"receipt"`
			Manifest   recovery.Manifest `json:"manifest"`
			UploadedAt time.Time         `json:"uploadedAt"`
		}{receipt, manifest, time.Now().UTC()}
		if err = json.NewEncoder(out).Encode(record); err != nil {
			return err
		}
		if err = out.Sync(); err != nil {
			return err
		}
		complete = true
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
	if err != nil {
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
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Operation         string            `json:"operation"`
		Manifest          recovery.Manifest `json:"manifest"`
		ValidationSeconds float64           `json:"validationSeconds"`
		CheckedAt         time.Time         `json:"checkedAt"`
	}{args[0], manifest, time.Since(started).Seconds(), time.Now().UTC()})
}

package api

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/recovery"
)

// recoveryProtection summarizes management-plane protection. Each field is either a
// proven observation or absent: an unverified property is never reported as healthy.
type recoveryProtection struct {
	HistoryConfigured bool              `json:"historyConfigured"`
	Records           []recovery.Record `json:"records"`
	// LastBackupAt is the creation time of the newest archive proven to be off-host.
	// A drill or restore never sets it, so verification cannot shrink the RPO.
	LastBackupAt          *time.Time `json:"lastBackupAt,omitempty"`
	LastBackupUploadedAt  *time.Time `json:"lastBackupUploadedAt,omitempty"`
	RecoveryPointSeconds  *float64   `json:"recoveryPointSeconds,omitempty"`
	LastRestoreTestedAt   *time.Time `json:"lastRestoreTestedAt,omitempty"`
	LastRestoreTestedFor  *time.Time `json:"lastRestoreTestedForBackupCreatedAt,omitempty"`
	LastVerificationAt    *time.Time `json:"lastVerificationAt,omitempty"`
	UnresolvedObservation bool       `json:"unresolvedObservation"`
}

func summarizeRecovery(dir string, records []recovery.Record) recoveryProtection {
	summary := recoveryProtection{HistoryConfigured: dir != "", Records: records}
	if summary.Records == nil {
		summary.Records = []recovery.Record{}
	}
	for i := range records {
		r := records[i]
		if r.Outcome == "unknown" {
			summary.UnresolvedObservation = true
		}
		if r.Outcome != "succeeded" {
			continue
		}
		if r.Kind == "backup" && summary.LastBackupAt == nil && !r.BackupCreatedAt.IsZero() {
			created := r.BackupCreatedAt
			summary.LastBackupAt = &created
			summary.LastBackupUploadedAt = r.UploadedAt
			age := time.Since(created).Seconds()
			summary.RecoveryPointSeconds = &age
		}
		if r.RestoreTestedAt != nil && summary.LastRestoreTestedAt == nil {
			summary.LastRestoreTestedAt = r.RestoreTestedAt
			if !r.BackupCreatedAt.IsZero() {
				tested := r.BackupCreatedAt
				summary.LastRestoreTestedFor = &tested
			}
		}
		if summary.LastVerificationAt == nil && r.ChecksumVerifiedAt != nil {
			summary.LastVerificationAt = r.ChecksumVerifiedAt
		}
	}
	return summary
}

// RegisterRecoveryProtectionRoutes exposes management-plane DR evidence and the
// explicit decision to let automation start again. Reading stays available in safe
// mode; resuming is an administrator decision that takes effect on the next start.
func (f *Fleet) registerRecoveryProtection(app *fiber.App) {
	admin := provisionAdmin(f.options.Auth)
	group := app.Group("/api/recovery", admin...)
	group.Get("/protection", func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		records, err := recovery.Load(f.options.RecoveryHistoryDir)
		if err != nil {
			return fiber.NewError(503, "Recovery history unavailable: "+err.Error())
		}
		return c.JSON(summarizeRecovery(f.options.RecoveryHistoryDir, records))
	})
	group.Post("/automation/resume", func(c *fiber.Ctx) error {
		var request struct {
			Reason string `json:"reason"`
		}
		if c.BodyParser(&request) != nil {
			return fiber.NewError(400, "Invalid request")
		}
		user, _ := c.Locals("user").(string)
		if user == "" {
			return fiber.NewError(403, "Administrator access required")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
		defer cancel()
		receipt, err := recovery.ResumeAutomation(ctx, f.options.Store, f.options.DataDir, user, request.Reason)
		if err != nil {
			f.auditRecovery(c, user, "failed")
			return fiber.NewError(409, err.Error())
		}
		f.mu.Lock()
		f.automationResumePending = true
		f.mu.Unlock()
		f.auditRecovery(c, user, "success")
		// Schedulers, collectors and background deliveries are wired when the process
		// starts. Saying "resumed" here would be a claim this process cannot keep.
		return c.JSON(fiber.Map{"resumedAt": receipt.ResumedAt, "actor": receipt.Actor, "reason": receipt.Reason,
			"automationPaused": true, "restartRequired": true,
			"message": "Automation resume recorded. Schedules, collectors and notification delivery start after the next TalosDeck restart."})
	})
}

func (f *Fleet) auditRecovery(c *fiber.Ctx, user, status string) {
	if f.options.Audit == nil {
		return
	}
	f.options.Audit.Log(audit.AuditEvent{Action: "recovery.automation.resume", User: user, IP: auth.GetClientIP(c), Status: status})
}

// automationResumeRecorded reports a resume decision taken during this process.
func (f *Fleet) automationResumeRecorded() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.automationResumePending
}

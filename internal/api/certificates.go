package api

import (
	"context"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/certificates"
)

type CertificateInspector interface {
	Check(context.Context) certificates.Report
}

// certificateMonitor coalesces concurrent readers and bounds network polling per
// cluster. Failed/cancelled requests never replace a completed cached snapshot.
type certificateMonitor struct {
	inspector CertificateInspector
	mu        sync.Mutex
	cached    certificates.Report
	expires   time.Time
	pending   chan struct{}
}

func (m *certificateMonitor) Check(ctx context.Context) certificates.Report {
	for {
		m.mu.Lock()
		if time.Now().Before(m.expires) {
			report := m.cached
			m.mu.Unlock()
			return report
		}
		if pending := m.pending; pending != nil {
			m.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return unavailableCertificates()
			}
		}
		if ctx.Err() != nil {
			m.mu.Unlock()
			return unavailableCertificates()
		}
		m.pending = make(chan struct{})
		m.mu.Unlock()
		bounded, cancel := context.WithTimeout(ctx, 25*time.Second)
		report := m.inspector.Check(bounded)
		cancelled := bounded.Err() != nil
		cancel()
		m.mu.Lock()
		if !cancelled {
			m.cached = report
			m.expires = time.Now().Add(5 * time.Minute)
		}
		close(m.pending)
		m.pending = nil
		m.mu.Unlock()
		return report
	}
}

func unavailableCertificates() certificates.Report {
	return certificates.Report{CheckedAt: time.Now().UTC(), Status: "unknown", Certificates: []certificates.Certificate{{ID: "collection", Name: "Certificate inspection", Source: "monitor", Status: "unknown", Reason: "unavailable", Verification: "unavailable", Error: "Certificate inspection was interrupted."}}, Summary: certificates.Summary{Unknown: 1}}
}

func (m *certificateMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		m.Check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func RegisterCertificateRoutes(router fiber.Router, inspector CertificateInspector, am *auth.AuthManager) {
	router.Get("/certificates", auth.RequireAuth(am), func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		if inspector == nil {
			return fiber.NewError(503, "Certificate monitoring unavailable")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		return c.JSON(inspector.Check(ctx))
	})
}

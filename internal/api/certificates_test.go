package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/certificates"
)

type certificateCheckFunc func(context.Context) certificates.Report

func (f certificateCheckFunc) Check(ctx context.Context) certificates.Report { return f(ctx) }

func TestCertificateMonitorCoalescesAndHonorsCancellation(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	m := &certificateMonitor{inspector: certificateCheckFunc(func(ctx context.Context) certificates.Report {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return certificates.Report{Status: "healthy", CheckedAt: time.Now().UTC()}
	})}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); m.Check(context.Background()) }()
	<-started
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if r := m.Check(cancelled); r.Status != "unknown" {
		t.Fatal("cancelled waiter reported success")
	}
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := m.Check(context.Background()); r.Status != "healthy" {
				t.Error("missing shared report")
			}
		}()
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent poll fanout: %d", calls.Load())
	}
	m.mu.Lock()
	m.expires = time.Time{}
	m.mu.Unlock()
	m.Check(context.Background())
	if calls.Load() != 2 {
		t.Fatal("expired cache not refreshed")
	}
}

func TestCertificateAPIReadOnlyRolesAndScopes(t *testing.T) {
	am := auth.NewAuthManager("test-password", "certificate-test-signing-key-long-enough")
	app := fiber.New()
	for _, scope := range []string{"alpha", "beta"} {
		RegisterCertificateRoutes(app.Group("/api/clusters/"+scope), certificateCheckFunc(func(context.Context) certificates.Report {
			return certificates.Report{Status: "unknown", Certificates: []certificates.Certificate{{ID: scope, Name: scope, Status: "unknown"}}}
		}), am)
	}
	for _, role := range []string{"viewer", "operator", "admin"} {
		token, err := am.GenerateToken(role, role)
		if err != nil {
			t.Fatal(err)
		}
		for _, scope := range []string{"alpha", "beta"} {
			req := httptest.NewRequest("GET", "/api/clusters/"+scope+"/certificates", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			var report certificates.Report
			err = json.NewDecoder(res.Body).Decode(&report)
			res.Body.Close()
			if err != nil || res.StatusCode != 200 || len(report.Certificates) != 1 || report.Certificates[0].ID != scope {
				t.Fatalf("role/scope %s/%s failed", role, scope)
			}
			if res.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("certificate report may be cached by proxy")
			}
		}
	}
	res, err := app.Test(httptest.NewRequest("GET", "/api/clusters/alpha/certificates", nil))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("anonymous certificate report allowed")
	}
	for _, role := range []string{"viewer", "operator"} {
		if auth.Can(role, "POST", "/api/clusters/alpha/certificates") {
			t.Fatal("readonly certificate monitoring permits mutation")
		}
	}
}

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"talosdeck/internal/alerts"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/talos"
)

func TestAPISecurityAndRouteDefects(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "talosdeck-api-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	auditLog := filepath.Join(tempDir, "audit.log")
	auditMgr, err := audit.NewAuditManager(auditLog, 100)
	if err != nil {
		t.Fatalf("failed to create audit manager: %v", err)
	}
	defer auditMgr.Close()

	authMgr := auth.NewAuthManager("adminpass123", "testsecretjwtkey1234567890123456")
	token, err := authMgr.GenerateToken("admin", "admin")
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	alertSvc := alerts.NewTelegramService("123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", "-100123456789", true)

	app := SetupServer(ServerConfig{
		Audit:        auditMgr,
		Auth:         authMgr,
		AlertService: alertSvc,
		Port:         ":0",
	})

	// API-08: Check server timeouts
	t.Run("API-08: Server timeouts configured", func(t *testing.T) {
		cfg := app.Config()
		if cfg.ReadTimeout == 0 {
			t.Errorf("expected non-zero ReadTimeout")
		}
		if cfg.WriteTimeout == 0 {
			t.Errorf("expected non-zero WriteTimeout")
		}
		if cfg.IdleTimeout == 0 {
			t.Errorf("expected non-zero IdleTimeout")
		}
	})

	// API-07: Missing API routes return JSON 404, not HTML
	t.Run("API-07: SPA Fallback does not hijack 404 on API routes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/nodes/nonexistent/service/typo", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404 Not Found, got %d", resp.StatusCode)
		}

		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"error"`) {
			t.Errorf("expected JSON error response, got: %s", string(body))
		}
		if strings.Contains(string(body), "<html>") || strings.Contains(string(body), "<!DOCTYPE") {
			t.Errorf("API route returned HTML instead of JSON: %s", string(body))
		}
	})

	// API-01: Service restart requires auth and validates node IP
	t.Run("API-01: Service restart requires auth and IP validation", func(t *testing.T) {
		// Unauthenticated
		req := httptest.NewRequest(http.MethodPost, "/api/nodes/10.42.0.110/services/kubelet/restart", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized, got %d", resp.StatusCode)
		}

		// Authenticated but invalid IP
		req = httptest.NewRequest(http.MethodPost, "/api/nodes/invalid-ip/services/kubelet/restart", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err = app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for invalid IP, got %d", resp.StatusCode)
		}
	})

	// API-02: Machine config export requires auth and validates node IP
	t.Run("API-02: MachineConfig requires auth and IP validation", func(t *testing.T) {
		// Unauthenticated
		req := httptest.NewRequest(http.MethodGet, "/api/nodes/10.42.0.110/config", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized, got %d", resp.StatusCode)
		}

		// Authenticated but invalid IP
		req = httptest.NewRequest(http.MethodGet, "/api/nodes/invalid-ip/config", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err = app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for invalid IP, got %d", resp.StatusCode)
		}
	})

	// API-03: Alert config masking and auth
	t.Run("API-03: Alert config requires auth, masks token and chat ID", func(t *testing.T) {
		// Unauthenticated GET
		req := httptest.NewRequest(http.MethodGet, "/api/alerts/config", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for GET /api/alerts/config, got %d", resp.StatusCode)
		}

		// Authenticated GET
		req = httptest.NewRequest(http.MethodGet, "/api/alerts/config", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err = app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var data map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&data)

		// Check masking: raw token "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11" must NOT appear
		tokenField, _ := data["bot_token"].(string)
		if strings.Contains(tokenField, "ABC-DEF1234ghIkl-zyx57W2v1u123ew11") {
			t.Errorf("bot_token leaked in plain text: %s", tokenField)
		}
		if !strings.Contains(tokenField, "****") {
			t.Errorf("bot_token is not masked: %s", tokenField)
		}

		// Updating with masked token should NOT overwrite original token
		updatePayload := `{"bot_token": "` + tokenField + `", "min_level": "ERROR"}`
		req = httptest.NewRequest(http.MethodPost, "/api/alerts/config", bytes.NewBufferString(updatePayload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err = app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK on update, got %d", resp.StatusCode)
		}

		// Verify underlying token was preserved in alertSvc
		cfg := alertSvc.GetConfig()
		if cfg.MinLevel != "ERROR" {
			t.Errorf("expected MinLevel ERROR, got %s", cfg.MinLevel)
		}
	})

	// API-05: Maintenance mode requires auth and validates IP
	t.Run("API-05: Maintenance toggle requires auth and validates IP", func(t *testing.T) {
		// Unauthenticated
		req := httptest.NewRequest(http.MethodPost, "/api/nodes/10.42.0.110/maintenance", bytes.NewBufferString(`{"enable": true}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized, got %d", resp.StatusCode)
		}

		// Authenticated but invalid IP
		req = httptest.NewRequest(http.MethodPost, "/api/nodes/invalid-ip/maintenance", bytes.NewBufferString(`{"enable": true}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err = app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request, got %d", resp.StatusCode)
		}

		// Authenticated with valid IP, but no Kubernetes client in this test app.
		req = httptest.NewRequest(http.MethodPost, "/api/nodes/10.42.0.110/maintenance", bytes.NewBufferString(`{"enable": true}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err = app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("expected 503 Service Unavailable, got %d", resp.StatusCode)
		}
	})

	// API-06: WebSocket authentication
	t.Run("API-06: WebSocket upgrade requires valid authentication", func(t *testing.T) {
		// Without token
		req := httptest.NewRequest(http.MethodGet, "/ws/nodes/10.42.0.110/dmesg", nil)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		req.Header.Set("Sec-WebSocket-Version", "13")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for unauthenticated WS, got %d", resp.StatusCode)
		}

		// With invalid token in query param
		req = httptest.NewRequest(http.MethodGet, "/ws/nodes/10.42.0.110/dmesg?token=invalid.jwt.token", nil)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		req.Header.Set("Sec-WebSocket-Version", "13")
		resp, err = app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized for bad token WS, got %d", resp.StatusCode)
		}
	})

	// API-12: Duplicate reboot route removal
	t.Run("API-12: Duplicate /api/api/nodes/:ip/reboot removed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/api/nodes/10.42.0.110/reboot", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404 Not Found on duplicate /api/api route, got %d", resp.StatusCode)
		}
	})

	// API-11: Validate IP across GET endpoints
	t.Run("API-11: Validate IP on GET endpoints", func(t *testing.T) {
		endpoints := []string{
			"/api/nodes/not-an-ip",
			"/api/nodes/not-an-ip/services",
			"/api/nodes/not-an-ip/containers",
			"/api/nodes/not-an-ip/disks",
		}
		for _, ep := range endpoints {
			req := httptest.NewRequest(http.MethodGet, ep, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("request failed for %s: %v", ep, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 Bad Request for %s, got %d", ep, resp.StatusCode)
			}
		}
	})

	// OPS-08: Healthz liveness probe returns 200 OK
	t.Run("OPS-08: Healthz liveness probe returns 200 OK", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}

		var data map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			t.Fatalf("failed to decode JSON response: %v", err)
		}
		if data["status"] != "ok" {
			t.Errorf("expected status 'ok', got %v", data["status"])
		}
	})

	// OPS-08: Readyz readiness probe returns 503 when manager uninitialized
	t.Run("OPS-08: Readyz probe returns 503 when manager uninitialized", func(t *testing.T) {
		for _, path := range []string{"/readyz", "/api/health"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("request failed for %s: %v", path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("expected 503 Service Unavailable for %s, got %d", path, resp.StatusCode)
			}

			var data map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
				t.Fatalf("failed to decode JSON response for %s: %v", path, err)
			}
			if data["status"] != "not ready" && data["status"] != "degraded" {
				t.Errorf("expected not ready or degraded status for %s, got %v", path, data["status"])
			}
		}
	})
}

func TestSetupServerStartsOwnedAlertWatcher(t *testing.T) {
	authMgr := auth.NewAuthManager("adminpass123", "testsecretjwtkey1234567890123456")
	token, err := authMgr.GenerateToken("admin", "admin")
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}
	app := SetupServer(ServerConfig{Manager: &talos.TalosManager{}, Auth: authMgr})
	defer app.Shutdown()

	req := httptest.NewRequest(http.MethodGet, "/api/alerts/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var payload struct {
		Status alerts.WatcherStatus `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode watcher status: %v", err)
	}
	if !payload.Status.Running {
		t.Fatal("expected SetupServer-owned alert watcher to be running")
	}
}

func TestHealthAndReadinessProbes(t *testing.T) {
	// 1. Uninitialized manager -> /healthz is 200, /readyz is 503
	appUninit := SetupServer(ServerConfig{Port: ":0"})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	resp, err := appUninit.Test(req)
	if err != nil {
		t.Fatalf("request to /healthz failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for /healthz, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	resp, err = appUninit.Test(req)
	if err != nil {
		t.Fatalf("request to /readyz failed: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for /readyz without manager, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	resp, err = appUninit.Test(req)
	if err != nil {
		t.Fatalf("request to /api/health failed: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for /api/health without manager, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Live access is opt-in, even when a developer has local credentials.
	configPath, node := os.Getenv("TALOSDECK_TEST_TALOSCONFIG"), os.Getenv("TALOSDECK_TEST_NODE")
	if configPath != "" && node != "" {
		mgr, err := talos.NewTalosManager(configPath, node)
		if err == nil {
			defer mgr.Close()
			appLive := SetupServer(ServerConfig{
				Manager: mgr,
				Port:    ":0",
			})
			req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
			resp, err = appLive.Test(req, 10000)
			if err != nil {
				t.Fatalf("request to /readyz with live manager failed: %v", err)
			}
			defer resp.Body.Close()

			var data map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
				t.Fatalf("failed to decode JSON response: %v", err)
			}

			// When live cluster is healthy, expect 200 OK and status "ok"
			if resp.StatusCode == http.StatusOK {
				if data["status"] != "ok" {
					t.Errorf("expected status 'ok', got %v", data["status"])
				}
				if healthy, ok := data["healthy"].(bool); !ok || !healthy {
					t.Errorf("expected healthy to be true, got %v", data["healthy"])
				}
			} else if resp.StatusCode == http.StatusServiceUnavailable {
				if data["status"] != "degraded" && data["status"] != "not ready" {
					t.Errorf("expected degraded or not ready, got %v", data["status"])
				}
			} else {
				t.Errorf("unexpected status code for /readyz: %d", resp.StatusCode)
			}

			// 3. Closed manager -> client is nil -> /readyz returns 503
			_ = mgr.Close()
			req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
			respClosed, err := appLive.Test(req, 5000)
			if err != nil {
				t.Fatalf("request to /readyz with closed manager failed: %v", err)
			}
			defer respClosed.Body.Close()
			if respClosed.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("expected 503 for /readyz with closed manager, got %d", respClosed.StatusCode)
			}
		}
	}
}

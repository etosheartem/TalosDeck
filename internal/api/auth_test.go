package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
)

func TestAuthAndAuditEndpoints(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "talosdeck-auth-test-*")
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

	app := SetupServer(ServerConfig{
		Audit: auditMgr,
		Auth:  authMgr,
		Port:  ":0",
	})

	// 1. GET /api/auth/me without token -> authenticated: false, role: viewer
	t.Run("GET /api/auth/me (unauthenticated)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
		var data map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&data)
		if data["authenticated"] != false {
			t.Errorf("expected authenticated=false, got %v", data["authenticated"])
		}
	})

	// 2. POST /api/auth/login with wrong password -> 401
	t.Run("POST /api/auth/login (invalid password)", func(t *testing.T) {
		body := bytes.NewBufferString(`{"password": "wrongpass"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized, got %d", resp.StatusCode)
		}
	})

	// 3. POST /api/auth/login with correct password -> 200 and token
	var token string
	t.Run("POST /api/auth/login (success)", func(t *testing.T) {
		body := bytes.NewBufferString(`{"password": "adminpass123"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
		var data struct {
			Token string `json:"token"`
			User  struct {
				Role     string `json:"role"`
				Username string `json:"username"`
			} `json:"user"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&data)
		if data.Token == "" {
			t.Errorf("expected token in response, got empty")
		}
		if data.User.Role != "admin" {
			t.Errorf("expected role 'admin', got %s", data.User.Role)
		}
		token = data.Token
	})

	// 4. GET /api/auth/me with valid token -> authenticated: true
	t.Run("GET /api/auth/me (authenticated)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
		var data struct {
			Authenticated bool `json:"authenticated"`
			User          struct {
				Role     string `json:"role"`
				Username string `json:"username"`
			} `json:"user"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&data)
		if !data.Authenticated {
			t.Errorf("expected authenticated=true")
		}
		if data.User.Role != "admin" {
			t.Errorf("expected role admin, got %s", data.User.Role)
		}
	})

	// 5. POST /api/nodes/:ip/reboot without token -> 401 Unauthorized
	t.Run("POST /api/nodes/10.42.0.110/reboot (unauthorized)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/nodes/10.42.0.110/reboot", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401 Unauthorized, got %d", resp.StatusCode)
		}
	})

	// 6. POST /api/auth/logout
	t.Run("POST /api/auth/logout", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
	})

	// 7. GET /api/audit -> verify events are recorded
	t.Run("GET /api/audit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/audit", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		var events []audit.AuditEvent
		if err := json.Unmarshal(body, &events); err != nil {
			t.Fatalf("failed to decode audit events: %v", err)
		}
		if len(events) == 0 {
			t.Errorf("expected audit events to be returned, got 0")
		}
		t.Logf("Found %d audit events", len(events))
	})
}

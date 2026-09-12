package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/clusters"
)

func TestSecurityRoutesEnforceRolesAndImmediateRevocation(t *testing.T) {
	dir := t.TempDir()
	store, err := clusters.Open(filepath.Join(dir, "db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := auth.NewPersistentAuthManager(store, "bootstrap-password-sentinel", "")
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	RegisterSecurityRoutes(app, manager, nil)
	request := func(method, path, token string, payload any) (int, map[string]any) {
		t.Helper()
		var body []byte
		if payload != nil {
			body, _ = json.Marshal(payload)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		var data map[string]any
		_ = json.Unmarshal(raw, &data)
		if bytes.Contains(raw, []byte("passwordHash")) {
			t.Fatal("password hash exposed")
		}
		return response.StatusCode, data
	}
	status, login := request("POST", "/api/auth/login", "", map[string]string{"password": "bootstrap-password-sentinel"})
	if status != 200 {
		t.Fatalf("legacy password-only login: %d", status)
	}
	admin := login["token"].(string)
	status, created := request("POST", "/api/auth/users", admin, map[string]string{"username": "reader", "password": "reader-password-sentinel", "role": "viewer"})
	if status != 201 {
		t.Fatalf("create user:%d", status)
	}
	id := created["user"].(map[string]any)["id"].(string)
	status, login = request("POST", "/api/auth/login", "", map[string]string{"username": "reader", "password": "reader-password-sentinel"})
	if status != 200 {
		t.Fatalf("named login:%d", status)
	}
	viewer := login["token"].(string)
	if status, _ = request("GET", "/api/auth/users", viewer, nil); status != 403 {
		t.Fatalf("viewer read users:%d", status)
	}
	if status, _ = request("POST", "/api/auth/users", viewer, map[string]string{"username": "evil", "password": "another-password", "role": "admin"}); status != 403 {
		t.Fatalf("viewer privilege escalation:%d", status)
	}
	status, me := request("GET", "/api/auth/me", viewer, nil)
	if status != 200 || me["authenticated"] != true {
		t.Fatal("named user me failed")
	}
	if status, _ = request("POST", "/api/auth/users/"+id+"/revoke", admin, map[string]any{}); status != 200 {
		t.Fatalf("session revoke:%d", status)
	}
	_, me = request("GET", "/api/auth/me", viewer, nil)
	if me["authenticated"] != false {
		t.Fatal("revoked token accepted by me")
	}
	if status, _ = request("POST", "/api/auth/logout", viewer, nil); status != 401 {
		t.Fatalf("revoked token accepted protected endpoint:%d", status)
	}
	if status, _ = request("PATCH", "/api/auth/users/"+id, admin, map[string]any{"disabled": true}); status != 200 {
		t.Fatalf("disable user:%d", status)
	}
	if status, _ = request("POST", "/api/auth/login", "", map[string]string{"username": "reader", "password": "reader-password-sentinel"}); status != 401 {
		t.Fatalf("disabled login:%d", status)
	}
	status, providers := request("GET", "/api/auth/providers", "", nil)
	if status != 200 || providers["oidc"].(map[string]any)["enabled"] != false {
		t.Fatal("public provider metadata failed")
	}
}

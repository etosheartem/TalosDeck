package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func TestAuthManager(t *testing.T) {
	am := NewAuthManager("secret123", "my-secret-key-1234567890123456")

	// 1. Password verification
	if !am.VerifyPassword("secret123") {
		t.Errorf("expected password verification to succeed")
	}
	if am.VerifyPassword("wrong") {
		t.Errorf("expected wrong password verification to fail")
	}

	// 2. Token generation and validation
	token, err := am.GenerateToken("admin", "admin")
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	claims, err := am.ValidateToken(token)
	if err != nil {
		t.Fatalf("failed to validate token: %v", err)
	}
	if claims.Username != "admin" || claims.Role != "admin" {
		t.Errorf("unexpected claims: %+v", claims)
	}

	// 3. Invalid token
	_, err = am.ValidateToken("invalid.token.here")
	if err == nil {
		t.Errorf("expected error for invalid token")
	}

	// 4. Fiber Middleware test
	app := fiber.New()
	app.Get("/protected", RequireAuth(am), func(c *fiber.Ctx) error {
		return c.SendString("ok:" + c.Locals("user").(string))
	})

	// 4a. Without header -> 401
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized without header, got %d", resp.StatusCode)
	}

	// 4b. With invalid header -> 401
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer badtoken")
	resp, _ = app.Test(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized with bad token, got %d", resp.StatusCode)
	}

	// 4c. With valid token -> 200 OK
	req = httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ = app.Test(req)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK with valid token, got %d", resp.StatusCode)
	}
}

func TestTokenExpiration(t *testing.T) {
	am := NewAuthManager("admin", "test-secret-key-1234567890123456")
	am.SetTokenTTL(1 * time.Millisecond) // expire almost immediately
	am.SetLeeway(0)                     // disable leeway for exact expiry testing

	token, err := am.GenerateToken("admin", "admin")
	if err != nil {
		t.Fatalf("token generation failed: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	_, err = am.ValidateToken(token)
	if err == nil {
		t.Errorf("expected token to expire")
	}
}

func TestTokenRevocation(t *testing.T) {
	am := NewAuthManager("admin", "test-secret-key-1234567890123456")
	token, err := am.GenerateToken("admin", "admin")
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	// Should be valid initially
	if _, err := am.ValidateToken(token); err != nil {
		t.Fatalf("expected token to be valid: %v", err)
	}

	// Revoke token
	if err := am.RevokeToken(token); err != nil {
		t.Fatalf("failed to revoke token: %v", err)
	}

	// Should now be rejected
	if _, err := am.ValidateToken(token); err == nil {
		t.Errorf("expected revoked token to be rejected")
	}
}

func TestEphemeralDefaults(t *testing.T) {
	// SEC-01: Without password and secret, ephemeral secure credentials must be generated
	am1 := NewAuthManager("", "")
	if am1.adminPassword == "admin" || len(am1.adminPassword) < 16 {
		t.Errorf("expected secure generated password, got %s", am1.adminPassword)
	}
	if string(am1.jwtSecret) == "talosdeck-default-secret-key-32-chars-long-jwt-auth" || len(am1.jwtSecret) < 32 {
		t.Errorf("expected secure random secret")
	}
}

func TestClientIPSpoofingProtection(t *testing.T) {
	app := fiber.New()
	var extractedIP string
	app.Get("/ip", func(c *fiber.Ctx) error {
		extractedIP = GetClientIP(c)
		return c.SendString(extractedIP)
	})

	// 1. Untrusted direct client attempting to spoof X-Forwarded-For:
	// In Fiber app.Test, direct peer IP is 0.0.0.0 (not loopback, not trusted).
	// Header must be ignored.
	req := httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	_, _ = app.Test(req)
	if extractedIP == "8.8.8.8" {
		t.Errorf("spoofed IP was accepted from untrusted direct peer!")
	}
	if extractedIP != "0.0.0.0" {
		t.Errorf("expected direct peer IP 0.0.0.0, got %s", extractedIP)
	}

	// 2. Trusted proxy forwarding client IP:
	t.Setenv("TALOSDECK_TRUSTED_PROXIES", "0.0.0.0")
	req = httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.195, 10.0.0.1")
	_, _ = app.Test(req)
	if extractedIP != "203.0.113.195" {
		t.Errorf("expected forwarded IP 203.0.113.195 from trusted proxy, got %s", extractedIP)
	}

	// 3. Malformed IP in X-Forwarded-For:
	req = httptest.NewRequest(http.MethodGet, "/ip", nil)
	req.Header.Set("X-Forwarded-For", "invalid-ip-string")
	_, _ = app.Test(req)
	if extractedIP != "0.0.0.0" {
		t.Errorf("expected fallback to direct IP 0.0.0.0 on invalid IP, got %s", extractedIP)
	}
}

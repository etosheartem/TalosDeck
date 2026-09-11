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
	am.tokenTTL = 1 * time.Millisecond // expire almost immediately

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

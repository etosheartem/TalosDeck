package api

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
)

// RegisterSecurityRoutes must be called before fleet dispatch and legacy auth
// routes. It preserves existing login/me response fields for older clients.
func RegisterSecurityRoutes(app *fiber.App, manager *auth.AuthManager, journal *audit.AuditManager) {
	if manager == nil {
		return
	}
	logEvent := func(c *fiber.Ctx, action, user, status string, details map[string]any) {
		if journal != nil {
			journal.Log(audit.AuditEvent{Action: action, User: user, IP: auth.GetClientIP(c), Status: status, Details: details})
		}
	}
	response := func(c *fiber.Ctx, token string, user auth.User) error {
		return c.JSON(fiber.Map{"token": token, "user": user, "expiresIn": 86400, "permissions": auth.Permissions(user.Role)})
	}
	tokenFrom := func(c *fiber.Ctx) string {
		parts := strings.SplitN(c.Get("Authorization"), " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1]
		}
		return ""
	}
	api := app.Group("/api/auth")
	rateLimit := limiter.New(limiter.Config{Max: 15, Expiration: time.Minute, KeyGenerator: auth.GetClientIP})
	api.Post("/login", rateLimit, func(c *fiber.Ctx) error {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if c.BodyParser(&req) != nil || req.Password == "" || len(req.Username) > 64 || len(req.Password) > 1024 {
			return fiber.NewError(400, "username and password required")
		}
		token, user, err := manager.Login(req.Username, req.Password)
		if err != nil {
			logEvent(c, "auth.login", "anonymous", "failure", nil)
			return fiber.NewError(401, "invalid username or password")
		}
		logEvent(c, "auth.login", user.Username, "success", map[string]any{"role": user.Role, "provider": user.Provider})
		return response(c, token, user)
	})
	api.Get("/me", func(c *fiber.Ctx) error {
		claims, err := manager.ValidateToken(tokenFrom(c))
		if err != nil {
			return c.JSON(fiber.Map{"authenticated": false, "user": fiber.Map{"username": "guest", "role": "viewer"}, "permissions": []string{}})
		}
		user := manager.UserByClaims(claims)
		return c.JSON(fiber.Map{"authenticated": true, "user": user, "permissions": auth.Permissions(user.Role)})
	})
	api.Post("/logout", auth.RequireAuth(manager), func(c *fiber.Ctx) error {
		user := auth.GetContextUser(c, manager)
		if err := manager.RevokeToken(tokenFrom(c)); err != nil {
			return fiber.NewError(503, "cannot revoke session")
		}
		logEvent(c, "auth.logout", user, "success", nil)
		return c.JSON(fiber.Map{"success": true})
	})
	api.Post("/password", auth.RequireAuth(manager), func(c *fiber.Ctx) error {
		var req struct {
			CurrentPassword string `json:"currentPassword"`
			Password        string `json:"password"`
		}
		if c.BodyParser(&req) != nil {
			return fiber.ErrBadRequest
		}
		user := auth.GetContextUser(c, manager)
		if err := manager.ChangePassword(user, req.CurrentPassword, req.Password); err != nil {
			return securityError(err)
		}
		logEvent(c, "auth.password.change", user, "success", nil)
		return c.JSON(fiber.Map{"success": true})
	})
	users := api.Group("/users", auth.RequireAuth(manager), func(c *fiber.Ctx) error {
		if c.Locals("role") != "admin" {
			return fiber.ErrForbidden
		}
		if !manager.Persistent() {
			return fiber.NewError(503, "persistent user management is not configured")
		}
		return c.Next()
	})
	users.Get("/", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"users": manager.ListUsers()}) })
	users.Post("/", func(c *fiber.Ctx) error {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if c.BodyParser(&req) != nil {
			return fiber.ErrBadRequest
		}
		user, err := manager.CreateUser(req.Username, req.Password, req.Role)
		if err != nil {
			return securityError(err)
		}
		logEvent(c, "users.create", auth.GetContextUser(c, manager), "success", map[string]any{"target": user.Username, "role": user.Role})
		return c.Status(201).JSON(fiber.Map{"user": user})
	})
	users.Patch("/:id", func(c *fiber.Ctx) error {
		var req struct {
			Role     *string `json:"role"`
			Disabled *bool   `json:"disabled"`
		}
		if c.BodyParser(&req) != nil || (req.Role == nil && req.Disabled == nil) {
			return fiber.ErrBadRequest
		}
		user, err := manager.UpdateUser(c.Params("id"), req.Role, req.Disabled)
		if err != nil {
			return securityError(err)
		}
		logEvent(c, "users.update", auth.GetContextUser(c, manager), "success", map[string]any{"target": user.Username, "role": user.Role, "disabled": user.Disabled})
		return c.JSON(fiber.Map{"user": user})
	})
	users.Post("/:id/password", func(c *fiber.Ctx) error {
		var req struct {
			Password string `json:"password"`
		}
		if c.BodyParser(&req) != nil {
			return fiber.ErrBadRequest
		}
		if err := manager.SetPassword(c.Params("id"), req.Password); err != nil {
			return securityError(err)
		}
		logEvent(c, "users.password.reset", auth.GetContextUser(c, manager), "success", map[string]any{"targetId": c.Params("id")})
		return c.JSON(fiber.Map{"success": true})
	})
	users.Post("/:id/revoke", func(c *fiber.Ctx) error {
		if err := manager.RevokeUser(c.Params("id")); err != nil {
			return securityError(err)
		}
		logEvent(c, "users.sessions.revoke", auth.GetContextUser(c, manager), "success", map[string]any{"targetId": c.Params("id")})
		return c.JSON(fiber.Map{"success": true})
	})
	api.Get("/providers", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"oidc": fiber.Map{"enabled": manager.OIDCEnabled(), "name": manager.OIDCName(), "loginUrl": "/api/auth/oidc/login"}})
	})
	cookie := func(c *fiber.Ctx, name, value string, maxAge int) {
		c.Cookie(&fiber.Cookie{Name: name, Value: value, Path: "/api/auth/oidc", HTTPOnly: true, Secure: manager.OIDCSecureCookie(), SameSite: "Lax", MaxAge: maxAge})
	}
	api.Get("/oidc/login", rateLimit, func(c *fiber.Ctx) error {
		location, state, err := manager.BeginOIDC()
		if err != nil {
			return fiber.NewError(503, "OIDC login is unavailable")
		}
		cookie(c, "talosdeck_oidc_state", state, 300)
		c.Set("Cache-Control", "no-store")
		return c.Redirect(location, 302)
	})
	api.Get("/oidc/callback", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
		defer cancel()
		code, binding, err := manager.FinishOIDC(ctx, c.Query("state"), c.Cookies("talosdeck_oidc_state"), c.Query("code"))
		cookie(c, "talosdeck_oidc_state", "", -1)
		if err != nil {
			logEvent(c, "auth.oidc", "anonymous", "failure", nil)
			return fiber.NewError(401, "OIDC login failed; restart sign-in")
		}
		cookie(c, "talosdeck_oidc_exchange", binding, 60)
		c.Set("Cache-Control", "no-store")
		c.Set("Referrer-Policy", "no-referrer")
		return c.Redirect(manager.OIDCOrigin()+"/#oidc_code="+url.QueryEscape(code), 302)
	})
	api.Post("/oidc/exchange", rateLimit, func(c *fiber.Ctx) error {
		if origin := c.Get("Origin"); origin != "" && origin != manager.OIDCOrigin() {
			return fiber.ErrForbidden
		}
		var req struct {
			Code string `json:"code"`
		}
		if c.BodyParser(&req) != nil {
			return fiber.ErrBadRequest
		}
		token, user, err := manager.ExchangeOIDC(req.Code, c.Cookies("talosdeck_oidc_exchange"))
		cookie(c, "talosdeck_oidc_exchange", "", -1)
		if err != nil {
			return fiber.NewError(401, "OIDC sign-in code is invalid or expired")
		}
		logEvent(c, "auth.oidc", user.Username, "success", map[string]any{"role": user.Role})
		c.Set("Cache-Control", "no-store")
		return response(c, token, user)
	})
}
func securityError(err error) error {
	if errors.Is(err, auth.ErrInvalidCredentials) {
		return fiber.NewError(401, "invalid credentials")
	}
	if errors.Is(err, auth.ErrUserNotFound) {
		return fiber.ErrNotFound
	}
	if errors.Is(err, auth.ErrConflict) {
		return fiber.NewError(409, err.Error())
	}
	return fiber.NewError(400, err.Error())
}

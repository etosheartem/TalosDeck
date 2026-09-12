package api

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
)

// Persist intent before accepting a mutation. A disk failure must not silently
// turn an installation into an unaudited control plane.
func auditMutationGuard(manager *audit.AuditManager, authentication *auth.AuthManager, fleet bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Path() == "/readyz" && (manager == nil || manager.Health() != nil) {
			return fiber.NewError(503, "Audit storage unavailable")
		}
		if c.Method() == "GET" || c.Method() == "HEAD" || c.Method() == "OPTIONS" {
			return c.Next()
		}
		// Scoped applications persist their own intent in the cluster journal.
		if fleet && strings.HasPrefix(c.Path(), "/api/clusters/") && len(strings.Split(strings.Trim(c.Path(), "/"), "/")) >= 4 {
			return c.Next()
		}
		if manager == nil || manager.Health() != nil {
			return fiber.NewError(503, "Audit storage unavailable; operation was not started")
		}
		user := "anonymous"
		parts := strings.SplitN(c.Get("Authorization"), " ", 2)
		if authentication != nil && len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if claims, err := authentication.ValidateToken(parts[1]); err == nil {
				user = claims.Username
			}
		}
		// No request body, query, arbitrary path or credentials enter the intent.
		if err := manager.Record(audit.AuditEvent{Action: "request." + strings.ToLower(c.Method()), User: user, IP: auth.GetClientIP(c), Status: "requested"}); err != nil {
			return fiber.NewError(503, "Audit storage unavailable; operation was not started")
		}
		return c.Next()
	}
}

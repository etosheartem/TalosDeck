package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// RecoveryRequired treats even malformed markers, directories and symlinks as
// recovery markers. Missing is the only permissive result. The result is latched
// for the lifetime of Fleet; removing the marker cannot unlock a running server.
func RecoveryRequired(dataDir string) (bool, error) {
	_, err := os.Lstat(filepath.Join(dataDir, "recovery-required.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot inspect recovery marker: %w", err)
	}
	return true, nil
}

func recoveryGuard(enabled bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !enabled {
			return c.Next()
		}
		path := strings.ToLower(c.Path())
		// No websocket transport is permitted after restore, including future command
		// transports. Ordinary authenticated HTTP reads remain available for review.
		blocked := strings.HasPrefix(path, "/ws/") || strings.Contains(path, "/ws/") || strings.EqualFold(c.Get("Upgrade"), "websocket")
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		default:
			switch path {
			case "/api/auth/login", "/api/auth/logout", "/api/auth/oidc/exchange":
			default:
				blocked = true
			}
		}
		if blocked {
			return c.Status(fiber.StatusLocked).JSON(fiber.Map{"error": "Restored management plane is in safe mode; recovery review is required before mutations", "code": "recovery_required"})
		}
		return c.Next()
	}
}

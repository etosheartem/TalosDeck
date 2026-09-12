package api

import (
	"context"
	"time"

	"github.com/gofiber/websocket/v2"
	"talosdeck/internal/auth"
)

// Revalidate both revocation/version and role policy while the connection lives.
// Closing the socket releases a blocked writer as well as cancelling Talos reads.
func watchWebSocketSession(ctx context.Context, c *websocket.Conn, manager *auth.AuthManager, cancel context.CancelFunc) func() {
	token, _ := c.Locals("authToken").(string)
	path, _ := c.Locals("authPath").(string)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if manager != nil {
				claims, err := manager.ValidateToken(token)
				if err != nil || claims == nil || !auth.Can(claims.Role, "GET", path) {
					cancel()
					_ = c.Close()
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

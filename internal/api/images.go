package api

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"talosdeck/internal/auth"
	"talosdeck/internal/imagefactory"
	"talosdeck/internal/talos"
)

type imageCatalog interface {
	Versions(context.Context) (imagefactory.Versions, error)
	Extensions(context.Context, string) (imagefactory.Catalog, error)
	Create(context.Context, imagefactory.Selection) (imagefactory.Profile, error)
	Resolve(context.Context, string, string) (imagefactory.Profile, error)
}

func imageError(err error) error {
	if errors.Is(err, imagefactory.ErrInvalid) {
		return fiber.NewError(422, "Unsupported Talos image selection")
	}
	return fiber.NewError(503, "Image Factory is unavailable or could not verify this image")
}
func RegisterImageRoutes(router fiber.Router, catalog imageCatalog, am *auth.AuthManager) {
	group := router.Group("/images", auth.RequireAuth(am))
	group.Use(func(c *fiber.Ctx) error {
		if catalog == nil {
			return fiber.NewError(503, "Image catalog unavailable")
		}
		c.Set("Cache-Control", "no-store")
		return c.Next()
	})
	group.Get("/versions", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
		defer cancel()
		value, err := catalog.Versions(ctx)
		if err != nil {
			return imageError(err)
		}
		return c.JSON(value)
	})
	group.Get("/extensions", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
		defer cancel()
		value, err := catalog.Extensions(ctx, c.Query("version"))
		if err != nil {
			return imageError(err)
		}
		return c.JSON(value)
	})
	group.Get("/schematics/:id", func(c *fiber.Ctx) error {
		ctx, cancel := context.WithTimeout(c.UserContext(), 25*time.Second)
		defer cancel()
		value, err := catalog.Resolve(ctx, c.Params("id"), c.Query("version"))
		if err != nil {
			return imageError(err)
		}
		return c.JSON(value)
	})
	group.Post("/schematics", func(c *fiber.Ctx) error {
		if c.Locals("role") != "admin" {
			return fiber.ErrForbidden
		}
		return c.Next()
	}, limiter.New(limiter.Config{Max: 10, Expiration: time.Minute, KeyGenerator: auth.GetClientIP}), func(c *fiber.Ctx) error {
		var input imagefactory.Selection
		if err := strictBody(c, &input); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		value, err := catalog.Create(ctx, input)
		if err != nil {
			return imageError(err)
		}
		return c.Status(201).JSON(value)
	})
}

type nodeImageInspector interface {
	ListNodes(context.Context) ([]*talos.NodeOverview, error)
	GetNodeImageStatus(context.Context, string) (talos.NodeImageStatus, error)
}

func RegisterNodeImageRoutes(router fiber.Router, inspector nodeImageInspector, am *auth.AuthManager) {
	router.Get("/nodes/:ip/extensions", auth.RequireAuth(am), func(c *fiber.Ctx) error {
		ip := net.ParseIP(c.Params("ip"))
		if ip == nil {
			return fiber.ErrBadRequest
		}
		if inspector == nil {
			return fiber.NewError(503, "Node image inspection unavailable")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
		defer cancel()
		nodes, err := inspector.ListNodes(ctx)
		if err != nil {
			return fiber.NewError(503, "Cannot verify cluster node inventory")
		}
		found := false
		for _, n := range nodes {
			if n != nil && ip.Equal(net.ParseIP(n.IP)) {
				found = true
				break
			}
		}
		if !found {
			return fiber.NewError(404, "Node not found in selected cluster")
		}
		value, err := inspector.GetNodeImageStatus(ctx, ip.String())
		if err != nil {
			return fiber.NewError(503, "Running image and extensions unavailable")
		}
		// Image references have no URL credentials. Do not expose an invalid value
		// taken from arbitrary MachineConfig text through a read-only inspector.
		if strings.Contains(value.InstallerImage, "://") || strings.ContainsAny(value.InstallerImage, "\r\n\t ") || strings.Contains(value.InstallerImage, "@") && !strings.Contains(value.InstallerImage, "@sha256:") {
			value.InstallerImage = ""
			value.Consistent = false
		}
		c.Set("Cache-Control", "no-store")
		return c.JSON(value)
	})
}

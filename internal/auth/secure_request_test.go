package auth

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestSecureRequestTrustsOnlyConfiguredSocketPeer(t *testing.T) {
	app := fiber.New(fiber.Config{ProxyHeader: "X-Forwarded-For"})
	secure := false
	app.Get("/", func(c *fiber.Ctx) error { secure = IsSecureRequest(c); return c.SendStatus(200) })
	t.Setenv("TALOSDECK_TRUSTED_PROXIES", "")
	request := func(proto string) {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Forwarded-Proto", proto)
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	request("https")
	if secure {
		t.Fatal("untrusted peer spoofed secure transport through forwarded headers")
	}
	t.Setenv("TALOSDECK_TRUSTED_PROXIES", "0.0.0.0")
	request("https")
	if !secure {
		t.Fatal("trusted ingress HTTPS was not recognized")
	}
	request("http")
	if secure {
		t.Fatal("plain HTTP classified as secure")
	}
	request("https,http")
	if secure {
		t.Fatal("ambiguous forwarded transport accepted")
	}
}

package api

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"talosdeck/internal/auth"
	"talosdeck/internal/imagefactory"
	"talosdeck/internal/talos"
)

type imageAPIFixture struct {
	creates int
	err     error
}

func (f *imageAPIFixture) Versions(context.Context) (imagefactory.Versions, error) {
	return imagefactory.Versions{}, f.err
}
func (f *imageAPIFixture) Extensions(context.Context, string) (imagefactory.Catalog, error) {
	return imagefactory.Catalog{}, f.err
}
func (f *imageAPIFixture) Resolve(context.Context, string, string) (imagefactory.Profile, error) {
	return imagefactory.Profile{}, f.err
}
func (f *imageAPIFixture) Create(context.Context, imagefactory.Selection) (imagefactory.Profile, error) {
	f.creates++
	return imagefactory.Profile{}, f.err
}
func TestImageCatalogRBACAndStrictRequests(t *testing.T) {
	am := auth.NewAuthManager("test-password", "test-jwt-key")
	f := &imageAPIFixture{}
	app := fiber.New()
	RegisterImageRoutes(app.Group("/api"), f, am)
	for _, tc := range []struct {
		role, method, path, body string
		status                   int
	}{
		{"viewer", "GET", "/images/versions", "", 200}, {"operator", "GET", "/images/extensions?version=v1.14.0", "", 200},
		{"viewer", "POST", "/images/schematics", "{}", 403}, {"operator", "POST", "/images/schematics", "{}", 403},
		{"admin", "POST", "/images/schematics", `{"version":"v1.14.0","kernelArgs":["secret"]}`, 400},
		{"admin", "POST", "/images/schematics", `{"version":"v1.14.0","architecture":"amd64","platform":"metal","extensions":[]}`, 201},
	} {
		token, _ := am.GenerateToken("fixture", tc.role)
		req := httptest.NewRequest(tc.method, "/api"+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("%s %s got %d want %d", tc.role, tc.path, res.StatusCode, tc.status)
		}
	}
	if f.creates != 1 {
		t.Fatalf("unauthorized or invalid request reached Factory: %d", f.creates)
	}
	f.err = errors.New("https://private.example/token=secret")
	token, _ := am.GenerateToken("fixture", "viewer")
	req := httptest.NewRequest("GET", "/api/images/versions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 503 || strings.Contains(string(body), "secret") {
		t.Fatal("upstream error disclosed")
	}
}

type nodeImageFixture struct{ requested string }

func (f *nodeImageFixture) ListNodes(context.Context) ([]*talos.NodeOverview, error) {
	return []*talos.NodeOverview{{IP: "10.42.0.110"}}, nil
}
func (f *nodeImageFixture) GetNodeImageStatus(_ context.Context, node string) (talos.NodeImageStatus, error) {
	f.requested = node
	return talos.NodeImageStatus{Node: node, InstallerImage: "https://user:secret@example.com/image", Consistent: true}, nil
}
func TestNodeImageInspectorRejectsForeignNodeAndRedactsInvalidImage(t *testing.T) {
	am := auth.NewAuthManager("test-password", "test-jwt-key")
	f := &nodeImageFixture{}
	app := fiber.New()
	RegisterNodeImageRoutes(app.Group("/api"), f, am)
	token, _ := am.GenerateToken("fixture", "viewer")
	for _, tc := range []struct {
		node   string
		status int
	}{{"bad", 400}, {"10.42.0.111", 404}, {"10.42.0.110", 200}} {
		req := httptest.NewRequest("GET", "/api/nodes/"+tc.node+"/extensions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status || strings.Contains(string(body), "secret") {
			t.Fatalf("node %s status %d", tc.node, res.StatusCode)
		}
	}
	if f.requested != "10.42.0.110" {
		t.Fatal("foreign node queried")
	}
}

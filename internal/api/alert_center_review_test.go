package api

import (
	"context"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"talosdeck/internal/alertcenter"
	"talosdeck/internal/auth"
	"talosdeck/internal/clusters"
	"testing"
)

func TestAlertCenterRoutesRBACIsolationAndSecretRedaction(t *testing.T) {
	dir := t.TempDir()
	store, err := clusters.Open(filepath.Join(dir, "db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	am := auth.NewAuthManager("testing-password", "testing-jwt-secret")
	app := fiber.New()
	first, second := uuid.NewString(), uuid.NewString()
	centers := map[string]*alertcenter.Center{}
	for _, id := range []string{first, second} {
		c, e := alertcenter.Open(context.Background(), store, id, nil)
		if e != nil {
			t.Fatal(e)
		}
		centers[id] = c
		RegisterAlertCenterRoutes(app.Group("/api/clusters/"+id), c, am, nil)
	}
	_, err = centers[first].SaveChannel(context.Background(), alertcenter.ChannelInput{Name: "fixture", Type: "webhook", Config: map[string]string{"url": "https://example.com/private-secret-marker"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		role, method, path, body string
		status                   int
	}{{"viewer", "GET", "/alerts", "", 200}, {"viewer", "GET", "/notifications/status", "", 200}, {"viewer", "GET", "/notifications/channels", "", 403}, {"operator", "GET", "/notifications/deliveries", "", 200}, {"operator", "POST", "/notifications/channels", "{}", 403}, {"viewer", "POST", "/alerts/silences", "{}", 403}, {"admin", "GET", "/notifications/channels", "", 200}, {"admin", "POST", "/notifications/channels", `{"unknown":"x"}`, 400}} {
		token, e := am.GenerateToken("fixture", tc.role)
		if e != nil {
			t.Fatal(e)
		}
		req := httptest.NewRequest(tc.method, "/api/clusters/"+first+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, e := app.Test(req)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("%s %s %s got %d want %d", tc.role, tc.method, tc.path, res.StatusCode, tc.status)
		}
		if strings.Contains(string(b), "private-secret-marker") {
			t.Fatal("secret disclosed")
		}
	}
	token, _ := am.GenerateToken("fixture", "admin")
	req := httptest.NewRequest("GET", "/api/clusters/"+second+"/notifications/channels", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if strings.Contains(string(b), "fixture") {
		t.Fatal("channel leaked across scope")
	}
	// Opening Alert Center before cluster insertion is deliberately supported.
	if list, e := store.List(context.Background()); e != nil || len(list) != 0 {
		t.Fatal("alert initialization unexpectedly registered a cluster")
	}
}

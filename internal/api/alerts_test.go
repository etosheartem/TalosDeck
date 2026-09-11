package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"talosdeck/internal/alerts"
)

func TestAlertsAPI_Config_Disabled(t *testing.T) {
	app := fiber.New()
	apiGroup := app.Group("/api")

	svc := alerts.NewTelegramService("", "", false)
	watcher := alerts.NewWatcher(nil, svc, 30*time.Second)

	RegisterAlertRoutes(apiGroup, svc, watcher)

	req := httptest.NewRequest(http.MethodGet, "/api/alerts/config", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var data map[string]interface{}
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &data)

	if data["enabled"] != false {
		t.Errorf("expected enabled=false, got %v", data["enabled"])
	}
	if data["bot_configured"] != false {
		t.Errorf("expected bot_configured=false, got %v", data["bot_configured"])
	}
	if data["chat_id"] != "" {
		t.Errorf("expected empty chat_id, got %v", data["chat_id"])
	}
}

func TestAlertsAPI_TestAlert_NotConfigured(t *testing.T) {
	app := fiber.New()
	apiGroup := app.Group("/api")

	svc := alerts.NewTelegramService("", "", false)
	watcher := alerts.NewWatcher(nil, svc, 30*time.Second)

	RegisterAlertRoutes(apiGroup, svc, watcher)

	req := httptest.NewRequest(http.MethodPost, "/api/alerts/test", bytes.NewBufferString(`{"message": "hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", resp.StatusCode)
	}
}

func TestAlertsAPI_TestAlert_Success(t *testing.T) {
	tgMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer tgMock.Close()

	app := fiber.New()
	apiGroup := app.Group("/api")

	svc := alerts.NewTelegramService("dummy:bot-token", "-100123456789", true)
	svc.SetAPIBaseURL(tgMock.URL)
	watcher := alerts.NewWatcher(nil, svc, 30*time.Second)

	RegisterAlertRoutes(apiGroup, svc, watcher)

	req := httptest.NewRequest(http.MethodPost, "/api/alerts/test", bytes.NewBufferString(`{"message": "Automated API test"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(body))
	}

	var data map[string]interface{}
	body, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(body, &data)

	if data["success"] != true {
		t.Errorf("expected success=true, got %v", data["success"])
	}
}

func TestAlertsAPI_UpdateConfig(t *testing.T) {
	app := fiber.New()
	apiGroup := app.Group("/api")

	svc := alerts.NewTelegramService("", "", false)
	watcher := alerts.NewWatcher(nil, svc, 30*time.Second)

	RegisterAlertRoutes(apiGroup, svc, watcher)

	updatePayload := `{"bot_token": "newtoken:12345678", "chat_id": "-100987654321", "enabled": true}`
	req := httptest.NewRequest(http.MethodPut, "/api/alerts/config", bytes.NewBufferString(updatePayload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	if !svc.IsConfigured() {
		t.Error("expected service to be configured after update")
	}
	if !svc.IsEnabled() {
		t.Error("expected service to be enabled after update")
	}
	if svc.GetMaskedChatID() != "-100****4321" {
		t.Errorf("expected masked chat id -100****4321, got %s", svc.GetMaskedChatID())
	}
}

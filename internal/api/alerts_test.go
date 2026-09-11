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

func TestAlertsAPI_PostConfig_MinLevel(t *testing.T) {
	app := fiber.New()
	apiGroup := app.Group("/api")

	svc := alerts.NewTelegramService("", "", false)
	watcher := alerts.NewWatcher(nil, svc, 30*time.Second)

	RegisterAlertRoutes(apiGroup, svc, watcher)

	// POST /api/alerts/config with min_level
	payload := `{"bot_token": "token:987654", "chat_id": "-100555666777", "enabled": true, "min_level": "WARNING"}`
	req := httptest.NewRequest(http.MethodPost, "/api/alerts/config", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	cfg := svc.GetConfig()
	if cfg.MinLevel != "WARNING" {
		t.Errorf("expected MinLevel WARNING, got %s", cfg.MinLevel)
	}

	// Verify GET /api/alerts/config returns all expected fields
	getReq := httptest.NewRequest(http.MethodGet, "/api/alerts/config", nil)
	getResp, err := app.Test(getReq)
	if err != nil {
		t.Fatalf("failed to GET config: %v", err)
	}
	defer getResp.Body.Close()

	var data map[string]interface{}
	body, _ := io.ReadAll(getResp.Body)
	_ = json.Unmarshal(body, &data)

	if data["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", data["enabled"])
	}
	if data["min_level"] != "WARNING" {
		t.Errorf("expected min_level WARNING, got %v", data["min_level"])
	}
	if _, ok := data["activeAlertsCount"]; !ok {
		t.Errorf("expected activeAlertsCount field in response")
	}
	if _, ok := data["lastCheckTime"]; !ok {
		t.Errorf("expected lastCheckTime field in response")
	}
	if _, ok := data["botToken"]; !ok {
		t.Errorf("expected botToken field in response")
	}
	if _, ok := data["chatID"]; !ok {
		t.Errorf("expected chatID field in response")
	}
}

func TestAlertsAPI_TestAlert_WithPayloadCredentials(t *testing.T) {
	tgMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer tgMock.Close()

	app := fiber.New()
	apiGroup := app.Group("/api")

	// Service initially unconfigured
	svc := alerts.NewTelegramService("", "", false)
	svc.SetAPIBaseURL(tgMock.URL)
	watcher := alerts.NewWatcher(nil, svc, 30*time.Second)

	RegisterAlertRoutes(apiGroup, svc, watcher)

	// Send test alert with credentials supplied directly in request body
	testPayload := `{"bot_token": "direct:token123", "chat_id": "-100999888777", "message": "Testing directly"}`
	req := httptest.NewRequest(http.MethodPost, "/api/alerts/test", bytes.NewBufferString(testPayload))
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

	// Verify alert was added to history
	histReq := httptest.NewRequest(http.MethodGet, "/api/alerts/history", nil)
	histResp, err := app.Test(histReq)
	if err != nil {
		t.Fatalf("failed to GET history: %v", err)
	}
	defer histResp.Body.Close()

	var history []alerts.AlertRecord
	histBody, _ := io.ReadAll(histResp.Body)
	_ = json.Unmarshal(histBody, &history)

	if len(history) == 0 {
		t.Errorf("expected at least 1 alert in history, got 0")
	}
}

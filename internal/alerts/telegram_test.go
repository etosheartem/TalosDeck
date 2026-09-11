package alerts

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMaskChatID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"123", "****"},
		{"12345", "****"},
		{"123456789", "12****789"},
		{"-1001987654321", "-100****4321"},
		{"-987654321", "-****321"},
	}

	for _, tt := range tests {
		res := MaskChatID(tt.input)
		if res != tt.expected {
			t.Errorf("MaskChatID(%q) = %q; want %q", tt.input, res, tt.expected)
		}
	}
}

func TestMaskToken(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"short", "****"},
		{"123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11", "123456:****"},
		{"abcdefghijklmnop", "abcd****nop"},
	}

	for _, tt := range tests {
		res := MaskToken(tt.input)
		if res != tt.expected {
			t.Errorf("MaskToken(%q) = %q; want %q", tt.input, res, tt.expected)
		}
	}
}

func TestTelegramService_SendAlert_MockServer(t *testing.T) {
	var requestCount int32
	var receivedPayload telegramPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)

		if !strings.Contains(r.URL.Path, "/bot12345:TOKEN/sendMessage") {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedPayload)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true, "result": {"message_id": 101}}`))
	}))
	defer server.Close()

	svc := NewTelegramService("12345:TOKEN", "-1001234567890", true)
	svc.SetAPIBaseURL(server.URL)

	// 1. Test SendAlert
	err := svc.SendAlert(LevelCritical, "Node Failure", "Node talos-worker-1 is offline")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if atomic.LoadInt32(&requestCount) != 1 {
		t.Fatalf("expected 1 request, got %d", requestCount)
	}
	if receivedPayload.ChatID != "-1001234567890" {
		t.Errorf("expected ChatID -1001234567890, got %s", receivedPayload.ChatID)
	}
	if !strings.Contains(receivedPayload.Text, "🚨") || !strings.Contains(receivedPayload.Text, "CRITICAL") {
		t.Errorf("expected text to have critical icon and title, got: %s", receivedPayload.Text)
	}

	// 2. Test SendNodeStatusAlert (Ready -> RECOVERED)
	err = svc.SendNodeStatusAlert("10.42.0.111", "talos-worker-1", "Ready", "Recovered")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(receivedPayload.Text, "✅") || !strings.Contains(receivedPayload.Text, "RECOVERED") {
		t.Errorf("expected recovered alert, got: %s", receivedPayload.Text)
	}

	// 3. Test SendNodeStatusAlert (NotReady -> CRITICAL)
	err = svc.SendNodeStatusAlert("10.42.0.111", "talos-worker-1", "NotReady", "Connection timeout")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(receivedPayload.Text, "🚨") || !strings.Contains(receivedPayload.Text, "CRITICAL") {
		t.Errorf("expected critical alert, got: %s", receivedPayload.Text)
	}

	// 4. Test SendEtcdAlert
	err = svc.SendEtcdAlert(false, "Loss of quorum")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(receivedPayload.Text, "etcd Cluster Degraded") {
		t.Errorf("expected etcd degraded text, got: %s", receivedPayload.Text)
	}

	err = svc.SendEtcdAlert(true, "All members healthy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(receivedPayload.Text, "etcd Cluster Healthy") {
		t.Errorf("expected etcd healthy text, got: %s", receivedPayload.Text)
	}

	// 5. Test SendTestMessage
	err = svc.SendTestMessage("Manual test from unit test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(receivedPayload.Text, "Test Notification") {
		t.Errorf("expected test notification, got: %s", receivedPayload.Text)
	}
}

func TestTelegramService_NotConfigured(t *testing.T) {
	svc := NewTelegramService("", "", false)
	if svc.IsConfigured() {
		t.Error("expected IsConfigured() to be false")
	}
	if svc.IsEnabled() {
		t.Error("expected IsEnabled() to be false")
	}

	err := svc.SendAlert(LevelCritical, "Test", "Message")
	if err != ErrNotConfigured {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}

func TestTelegramService_ConfigSaveAndLoad(t *testing.T) {
	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "alerts.json")

	svc := NewTelegramService("orig:token", "orig-chat", true)
	svc.SetConfigPath(cfgPath)

	err := svc.UpdateConfig(TelegramConfig{
		BotToken: "updated:token",
		ChatID:   "updated-chat",
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("failed to update config: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("failed to read saved config file: %v", err)
	}

	var saved TelegramConfig
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("failed to parse json: %v", err)
	}

	if saved.BotToken != "updated:token" || saved.ChatID != "updated-chat" || !saved.Enabled {
		t.Errorf("unexpected saved config: %+v", saved)
	}
}

func TestTelegramService_MessageTruncation(t *testing.T) {
	var receivedText string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p telegramPayload
		_ = json.Unmarshal(body, &p)
		receivedText = p.Text
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	svc := NewTelegramService("bot:token", "12345", true)
	svc.SetAPIBaseURL(server.URL)

	// Long message > 4096 chars (ALT-05)
	hugeMessage := strings.Repeat("Error line with detailed crash information\n", 200)
	err := svc.SendAlert(LevelCritical, "Huge Alert", hugeMessage)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len([]rune(receivedText)) > 4096 {
		t.Errorf("expected message <= 4096 chars, got %d", len([]rune(receivedText)))
	}
	if !strings.Contains(receivedText, "truncated") {
		t.Errorf("expected truncation notice in text: %s", receivedText)
	}
}

func TestTelegramService_HTMLNoRawTagsInTest(t *testing.T) {
	var receivedText string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p telegramPayload
		_ = json.Unmarshal(body, &p)
		receivedText = p.Text
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	svc := NewTelegramService("bot:token", "12345", true)
	svc.SetAPIBaseURL(server.URL)

	err := svc.SendTestNotification("", "", "Custom test message")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify no double-escaped &lt;b&gt; (ALT-07)
	if strings.Contains(receivedText, "&lt;b&gt;") || strings.Contains(receivedText, "&lt;/b&gt;") {
		t.Errorf("found broken escaped tags in telegram text: %s", receivedText)
	}
}

func TestTelegramService_RateLimitRetry(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok": false, "error_code": 429, "description": "Too Many Requests", "parameters": {"retry_after": 1}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	svc := NewTelegramService("bot:token", "12345", true)
	svc.SetAPIBaseURL(server.URL)

	err := svc.SendAlert(LevelInfo, "Rate limit test", "Should retry after 429")
	if err != nil {
		t.Fatalf("expected successful retry, got: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("expected 2 attempts after 429 retry, got %d", attempts)
	}
}

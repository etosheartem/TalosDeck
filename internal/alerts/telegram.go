package alerts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNotConfigured is returned when bot token or chat ID is missing.
	ErrNotConfigured = errors.New("telegram alerting is not configured")
	// ErrAlertsDisabled is returned when alerting is explicitly disabled.
	ErrAlertsDisabled = errors.New("telegram alerting is disabled")
)

// AlertLevel represents the severity level of an alert.
type AlertLevel string

const (
	LevelCritical  AlertLevel = "CRITICAL"
	LevelWarning   AlertLevel = "WARNING"
	LevelRecovered AlertLevel = "RECOVERED"
	LevelInfo      AlertLevel = "INFO"
)

// TelegramConfig represents the configuration settings for Telegram notifications.
type TelegramConfig struct {
	BotToken string `json:"botToken"`
	ChatID   string `json:"chatId"`
	Enabled  bool   `json:"enabled"`
}

// TelegramService handles sending notification alerts to a Telegram chat.
type TelegramService struct {
	client     *http.Client
	mu         sync.RWMutex
	botToken   string
	chatID     string
	enabled    bool
	configPath string
	apiBaseURL string // For testing mock servers
}

// NewTelegramService creates a new Telegram alerting service instance.
func NewTelegramService(botToken, chatID string, enabled bool) *TelegramService {
	return &TelegramService{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		botToken:   strings.TrimSpace(botToken),
		chatID:     strings.TrimSpace(chatID),
		enabled:    enabled,
		apiBaseURL: "https://api.telegram.org",
	}
}

// NewTelegramServiceFromEnv initializes the service reading from environment variables
// (TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID, TELEGRAM_ALERTS_ENABLED) and optional config file.
func NewTelegramServiceFromEnv() *TelegramService {
	cfgPath := os.Getenv("ALERTS_CONFIG_FILE")
	if cfgPath == "" {
		candidates := []string{
			"cluster-config/alerts.json",
			"alerts.json",
			"/etc/talosdeck/alerts.json",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				cfgPath = c
				break
			}
		}
	}

	var fileCfg TelegramConfig
	if cfgPath != "" {
		if data, err := os.ReadFile(cfgPath); err == nil {
			_ = json.Unmarshal(data, &fileCfg)
		}
	}

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		botToken = fileCfg.BotToken
	}

	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	if chatID == "" {
		chatID = fileCfg.ChatID
	}

	enabled := true
	if envEnabled := os.Getenv("TELEGRAM_ALERTS_ENABLED"); envEnabled != "" {
		enabled = strings.ToLower(envEnabled) == "true" || envEnabled == "1"
	} else if cfgPath != "" {
		enabled = fileCfg.Enabled
	}

	// If no credentials supplied yet, keep enabled=false until configured
	if botToken == "" || chatID == "" {
		enabled = false
	}

	svc := NewTelegramService(botToken, chatID, enabled)
	svc.configPath = cfgPath
	return svc
}

// IsConfigured returns true if both bot token and chat ID are set.
func (s *TelegramService) IsConfigured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.botToken != "" && s.chatID != ""
}

// IsEnabled returns true if alerts are enabled and credentials are present.
func (s *TelegramService) IsEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled && s.botToken != "" && s.chatID != ""
}

// GetConfig returns a copy of current configuration with masked bot token.
func (s *TelegramService) GetConfig() TelegramConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return TelegramConfig{
		BotToken: MaskToken(s.botToken),
		ChatID:   s.chatID,
		Enabled:  s.enabled,
	}
}

// GetMaskedChatID returns the masked representation of the configured chat ID.
func (s *TelegramService) GetMaskedChatID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return MaskChatID(s.chatID)
}

// UpdateConfig updates the in-memory configuration and persists to config file if set.
func (s *TelegramService) UpdateConfig(cfg TelegramConfig) error {
	s.mu.Lock()
	if cfg.BotToken != "" && !strings.Contains(cfg.BotToken, "*") {
		s.botToken = strings.TrimSpace(cfg.BotToken)
	}
	if cfg.ChatID != "" {
		s.chatID = strings.TrimSpace(cfg.ChatID)
	}
	s.enabled = cfg.Enabled
	path := s.configPath
	s.mu.Unlock()

	if path != "" {
		return s.saveToFile(path)
	}
	return nil
}

// SetConfigPath sets the path used to persist configuration changes.
func (s *TelegramService) SetConfigPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configPath = path
}

// SetAPIBaseURL allows overriding Telegram API endpoint (for mock server tests).
func (s *TelegramService) SetAPIBaseURL(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiBaseURL = url
}

func (s *TelegramService) saveToFile(path string) error {
	s.mu.RLock()
	cfg := TelegramConfig{
		BotToken: s.botToken,
		ChatID:   s.chatID,
		Enabled:  s.enabled,
	}
	s.mu.RUnlock()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal alerts config: %w", err)
	}

	return os.WriteFile(path, data, 0600)
}

// SendAlert sends a formatted HTML alert message to Telegram with icons.
func (s *TelegramService) SendAlert(level AlertLevel, title, message string) error {
	s.mu.RLock()
	token := s.botToken
	chatID := s.chatID
	enabled := s.enabled
	baseURL := s.apiBaseURL
	s.mu.RUnlock()

	if token == "" || chatID == "" {
		return ErrNotConfigured
	}
	if !enabled {
		return ErrAlertsDisabled
	}

	icon := GetIconForLevel(level)
	lvlStr := strings.ToUpper(string(level))

	formattedBody := formatMessageLines(message)
	timestamp := time.Now().UTC().Format("2006-01-02 15:04:05 UTC")

	text := fmt.Sprintf("%s <b>[%s] %s</b>\n\n%s\n\n<i>⏱ %s • TalosDeck</i>",
		icon,
		html.EscapeString(lvlStr),
		html.EscapeString(title),
		formattedBody,
		timestamp,
	)

	return s.sendRawTelegram(baseURL, token, chatID, text)
}

// SendNodeStatusAlert formats and dispatches an alert when a node status changes.
func (s *TelegramService) SendNodeStatusAlert(nodeIP, hostname, status, details string) error {
	var level AlertLevel
	var icon string

	switch strings.ToLower(status) {
	case "ready", "healthy", "ok":
		level = LevelRecovered
		icon = "✅"
	case "notready", "not ready", "offline", "unreachable", "down", "failed":
		level = LevelCritical
		icon = "🚨"
	case "maintenance", "cordoned", "drain", "draining", "degraded":
		level = LevelWarning
		icon = "⚠️"
	default:
		level = LevelWarning
		icon = "⚠️"
	}

	title := fmt.Sprintf("Node %s is %s", hostname, status)
	msg := fmt.Sprintf("Node: %s (%s)\nStatus: %s %s", nodeIP, hostname, icon, status)
	if details != "" {
		msg += fmt.Sprintf("\nDetails: %s", details)
	}

	return s.SendAlert(level, title, msg)
}

// SendEtcdAlert formats and dispatches an alert for etcd health state changes.
func (s *TelegramService) SendEtcdAlert(healthy bool, details string) error {
	if healthy {
		title := "etcd Cluster Healthy"
		msg := "Cluster quorum is fully restored and operational."
		if details != "" {
			msg += fmt.Sprintf("\nDetails: %s", details)
		}
		return s.SendAlert(LevelRecovered, title, msg)
	}

	title := "etcd Cluster Degraded"
	msg := "Critical alert: etcd cluster health or quorum degraded!"
	if details != "" {
		msg += fmt.Sprintf("\nDetails: %s", details)
	}
	return s.SendAlert(LevelCritical, title, msg)
}

// SendResourceAlert formats and dispatches an alert for high CPU or Memory usage.
func (s *TelegramService) SendResourceAlert(nodeIP, hostname, resourceType string, usagePercent int, details string) error {
	var level AlertLevel
	if usagePercent >= 90 {
		level = LevelCritical
	} else {
		level = LevelWarning
	}

	title := fmt.Sprintf("High %s Usage on %s (%d%%)", resourceType, hostname, usagePercent)
	msg := fmt.Sprintf("Node: %s (%s)\nResource: %s\nUsage: %d%%", nodeIP, hostname, resourceType, usagePercent)
	if details != "" {
		msg += fmt.Sprintf("\nDetails: %s", details)
	}

	return s.SendAlert(level, title, msg)
}

// SendTestMessage dispatches a test alert to verify Telegram configuration.
func (s *TelegramService) SendTestMessage(customText string) error {
	title := "Test Notification"
	msg := "This is a test notification from <b>TalosDeck Control Plane</b>.\nIntegration with Telegram bot is working correctly!"
	if customText != "" {
		msg += fmt.Sprintf("\nMessage: %s", customText)
	}
	return s.SendAlert(LevelInfo, title, msg)
}

// GetIconForLevel returns the appropriate emoji icon for the given alert level.
func GetIconForLevel(level AlertLevel) string {
	lvl := strings.ToUpper(string(level))
	switch {
	case strings.Contains(lvl, "CRIT") || strings.Contains(lvl, "ERR") || strings.Contains(lvl, "DOWN"):
		return "🚨"
	case strings.Contains(lvl, "WARN") || strings.Contains(lvl, "DEG"):
		return "⚠️"
	case strings.Contains(lvl, "RECOV") || strings.Contains(lvl, "OK") || strings.Contains(lvl, "RESOLV"):
		return "✅"
	case strings.Contains(lvl, "INFO"):
		return "ℹ️"
	default:
		return "🔔"
	}
}

// MaskChatID returns a masked version of the given chat ID for safe presentation.
func MaskChatID(chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return ""
	}
	if len(chatID) <= 5 {
		return "****"
	}
	// Telegram supergroup IDs usually start with -100
	if strings.HasPrefix(chatID, "-100") && len(chatID) > 8 {
		return "-100****" + chatID[len(chatID)-4:]
	}
	if strings.HasPrefix(chatID, "-") && len(chatID) > 6 {
		return "-****" + chatID[len(chatID)-3:]
	}
	if len(chatID) > 6 {
		return chatID[:2] + "****" + chatID[len(chatID)-3:]
	}
	return "****"
}

// MaskToken returns a masked version of the bot token.
func MaskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	parts := strings.Split(token, ":")
	if len(parts) == 2 {
		return parts[0] + ":****"
	}
	if len(token) > 8 {
		return token[:4] + "****" + token[len(token)-3:]
	}
	return "****"
}

type telegramPayload struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

type telegramAPIResponse struct {
	Ok          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code,omitempty"`
	Description string `json:"description,omitempty"`
}

func (s *TelegramService) sendRawTelegram(baseURL, token, chatID, text string) error {
	url := fmt.Sprintf("%s/bot%s/sendMessage", baseURL, token)

	payload := telegramPayload{
		ChatID:                chatID,
		Text:                  text,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal telegram payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read telegram response: %w", err)
	}

	var apiResp telegramAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err == nil {
		if !apiResp.Ok {
			return fmt.Errorf("telegram API error (%d): %s", apiResp.ErrorCode, apiResp.Description)
		}
	} else if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram HTTP error %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func formatMessageLines(content string) string {
	lines := strings.Split(content, "\n")
	var formatted []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			formatted = append(formatted, "")
			continue
		}

		// Check if line is Key: Value
		if idx := strings.Index(trimmed, ": "); idx > 0 && !strings.Contains(trimmed[:idx], " ") {
			key := trimmed[:idx]
			val := trimmed[idx+2:]
			formatted = append(formatted, fmt.Sprintf("<b>%s:</b> %s", html.EscapeString(key), html.EscapeString(val)))
		} else {
			formatted = append(formatted, html.EscapeString(trimmed))
		}
	}

	return strings.Join(formatted, "\n")
}

package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AuditEvent represents a logged security, administrative, or operational action.
type AuditEvent struct {
	ID        string         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Action    string         `json:"action"` // e.g. "node.reboot", "backup.create", "worker.create", "auth.login"
	User      string         `json:"user"`   // e.g. "admin", "viewer", "system"
	IP        string         `json:"ip"`     // client IP address
	Status    string         `json:"status"` // "success" or "failed"
	Details   map[string]any `json:"details,omitempty"`
}

// AuditManager is a thread-safe manager for recording and querying audit events.
// It maintains the latest N events in memory and writes all events to an append-only log file.
type AuditManager struct {
	mu         sync.RWMutex
	filePath   string
	maxEntries int
	events     []AuditEvent
	logFile    *os.File
}

// NewAuditManager creates an AuditManager writing to filePath, retaining maxEntries in memory.
func NewAuditManager(filePath string, maxEntries int) (*AuditManager, error) {
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	if filePath == "" {
		filePath = "./data/audit.log"
	}

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create audit log directory: %w", err)
	}

	var loadedEvents []AuditEvent

	// Load existing entries if the file exists with extended buffer (SEC-10)
	if f, err := os.Open(filePath); err == nil {
		scanner := bufio.NewScanner(f)
		const maxScanBuffer = 10 * 1024 * 1024 // 10MB max line buffer
		scanner.Buffer(make([]byte, 64*1024), maxScanBuffer)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var ev AuditEvent
			if err := json.Unmarshal([]byte(line), &ev); err == nil {
				loadedEvents = append(loadedEvents, ev)
			}
		}
		_ = f.Close()

		// Keep only the latest maxEntries
		if len(loadedEvents) > maxEntries {
			loadedEvents = loadedEvents[len(loadedEvents)-maxEntries:]
		}
	}

	// Open for appending with 0600 permissions (SEC-11)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit log file: %w", err)
	}
	_ = os.Chmod(filePath, 0600)

	return &AuditManager{
		filePath:   filePath,
		maxEntries: maxEntries,
		events:     loadedEvents,
		logFile:    f,
	}, nil
}

// Log records an audit event safely into memory and the persistent file.
func (m *AuditManager) Log(event AuditEvent) {
	if event.ID == "" {
		event.ID = uuid.New().String()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.User == "" {
		event.User = "system"
	}
	if event.Status == "" {
		event.Status = "success"
	}

	// Defensive copy & sanitize Details (SEC-04, SEC-12)
	event.Details = copyAndSanitizeDetails(event.Details)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Append to in-memory ring buffer
	m.events = append(m.events, event)
	if len(m.events) > m.maxEntries {
		m.events = m.events[len(m.events)-m.maxEntries:]
	}

	// Write JSON line to disk without synchronous fsync under lock (SEC-05)
	if m.logFile != nil {
		if data, err := json.Marshal(event); err == nil {
			_, _ = m.logFile.Write(append(data, '\n'))
		}
	}
}

// GetEvents returns filtered audit events in reverse chronological order (newest first).
// Returns deep copies of events and details to prevent data races (SEC-04).
func (m *AuditManager) GetEvents(limit int, action string, search string) []AuditEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 || limit > m.maxEntries {
		limit = 50
	}

	action = strings.TrimSpace(strings.ToLower(action))
	search = strings.TrimSpace(strings.ToLower(search))

	result := make([]AuditEvent, 0, limit)

	// Iterate backwards for newest first
	for i := len(m.events) - 1; i >= 0; i-- {
		ev := m.events[i]

		// Filter by action if provided
		if action != "" && !strings.Contains(strings.ToLower(ev.Action), action) {
			continue
		}

		// Filter by search keyword if provided
		if search != "" {
			match := strings.Contains(strings.ToLower(ev.Action), search) ||
				strings.Contains(strings.ToLower(ev.User), search) ||
				strings.Contains(strings.ToLower(ev.IP), search) ||
				strings.Contains(strings.ToLower(ev.Status), search)

			if !match && len(ev.Details) > 0 {
				if dBytes, err := json.Marshal(ev.Details); err == nil {
					match = strings.Contains(strings.ToLower(string(dBytes)), search)
				}
			}

			if !match {
				continue
			}
		}

		evCopy := ev
		evCopy.Details = deepCopyDetails(ev.Details)
		result = append(result, evCopy)
		if len(result) >= limit {
			break
		}
	}

	return result
}

// TotalCount returns total events currently in memory.
func (m *AuditManager) TotalCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.events)
}

// Close flushes and closes the underlying log file.
func (m *AuditManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.logFile != nil {
		_ = m.logFile.Sync()
		err := m.logFile.Close()
		m.logFile = nil
		return err
	}
	return nil
}

var sensitiveKeyPatterns = []string{
	"password", "secret", "token", "auth", "cookie",
	"talosconfig", "kubeconfig", "private", "credential",
}

func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, p := range sensitiveKeyPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func sanitizeStringValue(val string) string {
	lower := strings.ToLower(val)
	patterns := []string{"bearer ", "token=", "password=", "secret="}
	for _, p := range patterns {
		if idx := strings.Index(lower, p); idx != -1 {
			end := strings.IndexAny(val[idx+len(p):], " \t\r\n,;\"'")
			if end == -1 {
				val = val[:idx+len(p)] + "***MASKED***"
			} else {
				val = val[:idx+len(p)] + "***MASKED***" + val[idx+len(p)+end:]
			}
			lower = strings.ToLower(val)
		}
	}
	return val
}

func copyAndSanitizeDetails(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		if isSensitiveKey(k) {
			dst[k] = "***MASKED***"
		} else if str, ok := v.(string); ok {
			dst[k] = sanitizeStringValue(str)
		} else if nestedMap, ok := v.(map[string]any); ok {
			dst[k] = copyAndSanitizeDetails(nestedMap)
		} else {
			dst[k] = v
		}
	}
	return dst
}

func deepCopyDetails(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		if nestedMap, ok := v.(map[string]any); ok {
			dst[k] = deepCopyDetails(nestedMap)
		} else {
			dst[k] = v
		}
	}
	return dst
}

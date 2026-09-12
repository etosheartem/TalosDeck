package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AuditEvent represents a logged security, administrative, or operational action.
type AuditEvent struct {
	ClusterID string         `json:"clusterId,omitempty"`
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
	clusterID  string
	mu         sync.RWMutex
	filePath   string
	maxEntries int
	events     []AuditEvent
	logFile    *os.File
	healthErr  error
}

const maxAuditLineBytes = 10 * 1024 * 1024

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

	loadedEvents := make([]AuditEvent, 0, maxEntries)
	next := 0

	// Load existing entries if the file exists with extended buffer (SEC-10)
	if f, err := os.Open(filePath); err == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), maxAuditLineBytes)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var ev AuditEvent
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("invalid audit record at line %d", lineNumber)
			}
			if len(loadedEvents) < maxEntries {
				loadedEvents = append(loadedEvents, ev)
			} else {
				loadedEvents[next] = ev
				next = (next + 1) % maxEntries
			}
		}
		readErr, closeErr := scanner.Err(), f.Close()
		if readErr != nil {
			return nil, fmt.Errorf("cannot read audit journal: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("cannot close audit journal reader: %w", closeErr)
		}
		if next > 0 {
			ordered := make([]AuditEvent, 0, len(loadedEvents))
			ordered = append(ordered, loadedEvents[next:]...)
			ordered = append(ordered, loadedEvents[:next]...)
			loadedEvents = ordered
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("cannot read audit journal: %w", err)
	}

	// Open for appending with 0600 permissions (SEC-11)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit log file: %w", err)
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cannot protect audit journal: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cannot sync audit journal: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cannot open audit directory: %w", err)
	}
	dirErr := directory.Sync()
	closeDirErr := directory.Close()
	if err := errors.Join(dirErr, closeDirErr); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cannot sync audit directory: %w", err)
	}

	return &AuditManager{
		filePath:   filePath,
		maxEntries: maxEntries,
		events:     loadedEvents,
		logFile:    f,
	}, nil
}

// Log preserves the legacy call signature while surfacing durable write errors.
func (m *AuditManager) Log(event AuditEvent) {
	if err := m.Record(event); err != nil {
		log.Printf("[AUDIT] record failed: %v", err)
	}
}

// Record durably appends before publishing in memory. Any failure is sticky:
// callers must stop mutations and repair/reopen the journal, never hide loss.
func (m *AuditManager) Record(event AuditEvent) error {
	if m == nil {
		return errors.New("audit journal unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.healthErr != nil {
		return m.healthErr
	}
	if m.logFile == nil {
		m.healthErr = errors.New("audit journal is closed")
		return m.healthErr
	}
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

	if m.clusterID != "" {
		event.ClusterID = m.clusterID
	}
	// JSON normalization validates unsupported/cyclic values before recursive
	// sanitization and deep-copies typed maps/slices too. Errors never include data.
	raw, err := json.Marshal(event)
	if err != nil {
		m.healthErr = errors.New("cannot encode audit event")
		return m.healthErr
	}
	if len(raw)+1 > maxAuditLineBytes {
		m.healthErr = errors.New("audit event exceeds journal record limit")
		return m.healthErr
	}
	if err = json.Unmarshal(raw, &event); err != nil {
		m.healthErr = errors.New("cannot normalize audit event")
		return m.healthErr
	}
	event.Details = copyAndSanitizeDetails(event.Details)
	data, err := json.Marshal(event)
	if err != nil || len(data)+1 > maxAuditLineBytes {
		m.healthErr = errors.New("cannot encode sanitized audit event")
		return m.healthErr
	}
	data = append(data, '\n')
	n, err := m.logFile.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = m.logFile.Sync()
	}
	if err != nil {
		m.healthErr = fmt.Errorf("cannot persist audit event: %w", err)
		return m.healthErr
	}
	if len(m.events) == m.maxEntries {
		copy(m.events, m.events[1:])
		m.events[len(m.events)-1] = event
	} else {
		m.events = append(m.events, event)
	}
	return nil
}

func (m *AuditManager) Health() error {
	if m == nil {
		return errors.New("audit journal unavailable")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.healthErr != nil {
		return m.healthErr
	}
	if m.logFile == nil {
		return errors.New("audit journal is closed")
	}
	return nil
}

// BindCluster labels new records and historical entries returned from the
// cluster's dedicated journal. It does not rewrite the legacy journal on disk.
func (m *AuditManager) BindCluster(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clusterID = id
	for i := range m.events {
		m.events[i].ClusterID = id
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
		syncErr := m.logFile.Sync()
		closeErr := m.logFile.Close()
		m.logFile = nil
		if err := errors.Join(syncErr, closeErr); err != nil {
			m.healthErr = errors.Join(m.healthErr, err)
		}
		return m.healthErr
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
	for _, pattern := range []string{"bearer ", "token=", "password=", "secret="} {
		// Scan immutable input and advance beyond each match, including empty
		// values. Never search replacement text: that would match forever.
		lower := strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'Z' {
				return r + ('a' - 'A')
			}
			return r
		}, val)
		var out strings.Builder
		offset := 0
		for {
			relative := strings.Index(lower[offset:], pattern)
			if relative < 0 {
				out.WriteString(val[offset:])
				break
			}
			start := offset + relative + len(pattern)
			end := strings.IndexAny(val[start:], " \t\r\n,;\"'&")
			if end < 0 {
				end = len(val)
			} else {
				end += start
			}
			out.WriteString(val[offset:start])
			out.WriteString("***MASKED***")
			offset = end
		}
		val = out.String()
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
		} else {
			dst[k] = copyAndSanitizeValue(v)
		}
	}
	return dst
}

func copyAndSanitizeValue(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitizeStringValue(typed)
	case map[string]any:
		return copyAndSanitizeDetails(typed)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = copyAndSanitizeValue(item)
		}
		return result
	default:
		return typed
	}
}

func deepCopyDetails(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = deepCopyDetailValue(v)
	}
	return dst
}

func deepCopyDetailValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return deepCopyDetails(typed)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = deepCopyDetailValue(item)
		}
		return result
	default:
		return typed
	}
}

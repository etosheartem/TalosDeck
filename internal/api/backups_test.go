package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"talosdeck/internal/auth"
	"talosdeck/internal/backup"
	"talosdeck/internal/talos"
)

func TestBackupAPIEndpoints(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "talosdeck-api-backup-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	bm, err := backup.NewBackupManager(tempDir, nil)
	if err != nil {
		t.Fatalf("failed to init backup manager: %v", err)
	}

	app := fiber.New()
	apiGroup := app.Group("/api")
	RegisterBackupRoutes(apiGroup, bm)

	// 1. GET /api/backups -> initially empty array []
	req := httptest.NewRequest(http.MethodGet, "/api/backups", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET /api/backups failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var list []backup.BackupInfo
	bodyBytes, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(bodyBytes, &list); err != nil {
		t.Fatalf("failed to unmarshal backups: %v, body: %s", err, string(bodyBytes))
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 backups, got %d", len(list))
	}

	// 2. Prepare a mock backup file in tempDir
	testFileName := "mock-backup-20260911.snapshot"
	testContent := []byte("etcd database snapshot mock test payload")
	testPath := filepath.Join(tempDir, testFileName)
	if err := os.WriteFile(testPath, testContent, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	_ = os.WriteFile(testPath+".sha256", []byte("a1b2c3d4  "+testFileName+"\n"), 0644)
	mockInfo := backup.BackupInfo{
		ID:          testFileName,
		Filename:    testFileName,
		Size:        int64(len(testContent)),
		HumanSize:   "39 B",
		Timestamp:   time.Now().UTC(),
		Type:        backup.BackupTypeEtcd,
		Checksum:    "a1b2c3d4",
		ClusterName: "test-cluster",
	}
	mockMeta, _ := json.Marshal(mockInfo)
	_ = os.WriteFile(testPath+".json", mockMeta, 0644)

	// 3. GET /api/backups -> should list the mock backup
	req = httptest.NewRequest(http.MethodGet, "/api/backups", nil)
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("GET /api/backups failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	bodyBytes, _ = io.ReadAll(resp.Body)
	list = nil
	if err := json.Unmarshal(bodyBytes, &list); err != nil {
		t.Fatalf("failed to unmarshal backups: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(list))
	}
	if list[0].Filename != testFileName {
		t.Errorf("expected filename %s, got %s", testFileName, list[0].Filename)
	}
	if list[0].Checksum != "a1b2c3d4" {
		t.Errorf("expected checksum a1b2c3d4, got %s", list[0].Checksum)
	}

	// 4. GET /api/backups/:id/download -> download file
	req = httptest.NewRequest(http.MethodGet, "/api/backups/"+testFileName+"/download", nil)
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("GET /api/backups/:id/download failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	dlBytes, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(dlBytes, testContent) {
		t.Errorf("downloaded content does not match: got %s, expected %s", string(dlBytes), string(testContent))
	}

	// 5. POST /api/backups/create with invalid type -> 400 Bad Request
	createBadBody := bytes.NewBufferString(`{"type": "invalid-type"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/backups/create", createBadBody)
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("POST /api/backups/create failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", resp.StatusCode)
	}

	// 6. DELETE /api/backups/:id -> removes backup
	req = httptest.NewRequest(http.MethodDelete, "/api/backups/"+testFileName, nil)
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("DELETE /api/backups/:id failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Verify file is gone
	if _, err := os.Stat(testPath); !os.IsNotExist(err) {
		t.Errorf("expected file to be deleted from disk")
	}

	// 7. GET /api/backups/:id/download for deleted file -> 404
	req = httptest.NewRequest(http.MethodGet, "/api/backups/"+testFileName+"/download", nil)
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("download non-existent failed: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestBackupAPILiveCreation(t *testing.T) {
	configPath := "/home/artem/laba-kuber/cluster-config/talosconfig"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Skip("talosconfig not found, skipping live API test")
	}
	mgr, err := talos.NewTalosManager(configPath, "10.42.0.110")
	if err != nil {
		t.Skipf("failed to connect to Talos cluster: %v", err)
	}
	defer mgr.Close()

	app := SetupServer(ServerConfig{
		Manager: mgr,
		Port:    ":0",
	})
	if app == nil {
		t.Fatal("failed to setup server")
	}

	token, err := auth.NewAuthManagerFromEnv().GenerateToken("admin", "admin")
	if err != nil {
		t.Fatalf("failed to generate test auth token: %v", err)
	}

	// Trigger full cluster backup creation via POST /api/backups/create
	reqBody := bytes.NewBufferString(`{"type": "etcd", "node": "10.42.0.110"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/backups/create", reqBody)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req, 60000) // 60s timeout
	if err != nil {
		t.Fatalf("POST /api/backups/create failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201 Created, got %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Status  string            `json:"status"`
		Message string            `json:"message"`
		Backup  backup.BackupInfo `json:"backup"`
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	t.Logf("Created backup via API: ID=%s, Filename=%s, Size=%s", result.Backup.ID, result.Backup.Filename, result.Backup.HumanSize)

	// Clean up backup after test
	delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/backups/%s", result.Backup.ID), nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	delResp, delErr := app.Test(delReq)
	if delErr == nil && delResp.StatusCode == http.StatusOK {
		t.Logf("Cleaned up backup %s", result.Backup.ID)
	}
}

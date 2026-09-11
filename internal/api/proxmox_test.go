package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"talosdeck/internal/proxmox"
)

type testNodeDrainer func(context.Context, string) error

func (f testNodeDrainer) CordonAndDrainNode(ctx context.Context, nodeName string) error {
	return f(ctx, nodeName)
}

func TestProxmoxAPIStatusUnconfigured(t *testing.T) {
	app := fiber.New()
	apiGroup := app.Group("/api")
	RegisterProxmoxRoutes(apiGroup, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/proxmox/status", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if data["configured"] != false {
		t.Errorf("Expected configured=false, got %v", data["configured"])
	}
}

func TestProxmoxAPIConfigured(t *testing.T) {
	var vmStopped bool

	// Mock Proxmox backend
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/api2/json/nodes/pve/status":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"cpu":    0.05,
					"uptime": 1000,
					"cpuinfo": map[string]interface{}{
						"model":   "Intel Test CPU",
						"cores":   4,
						"cpus":    8,
						"sockets": 1,
					},
					"memory": map[string]interface{}{
						"total":     uint64(16000000000),
						"used":      uint64(8000000000),
						"free":      uint64(8000000000),
						"available": uint64(8000000000),
					},
				},
			})
		case path == "/api2/json/nodes/pve/storage/local-lvm/status":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"type":    "lvmthin",
					"total":   uint64(100000000000),
					"used":    uint64(30000000000),
					"avail":   uint64(70000000000),
					"active":  1,
					"enabled": 1,
				},
			})
		case path == "/api2/json/cluster/nextid":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": "114"})
		case path == "/api2/json/nodes/pve/qemu":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": "UPID:pve:0001:0001:0001:qmcreate:114:root@pam:",
			})
		case strings.HasPrefix(path, "/api2/json/nodes/pve/tasks/"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"status":     "stopped",
					"exitstatus": "OK",
				},
			})
		case path == "/api2/json/nodes/pve/qemu/114/status/shutdown":
			vmStopped = true
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": "UPID:pve:shutdown:114"})
		case path == "/api2/json/nodes/pve/qemu/114/status/stop":
			vmStopped = true
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": "UPID:pve:stop:114"})
		case path == "/api2/json/nodes/pve/qemu/114/status/current":
			status := "running"
			if vmStopped {
				status = "stopped"
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"vmid":   114,
					"name":   "talos-worker-3",
					"status": status,
				},
			})
		case path == "/api2/json/nodes/pve/qemu/110/status/current":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"vmid":   110,
					"name":   "talos-cp-1",
					"status": "running",
				},
			})
		case path == "/api2/json/nodes/pve/qemu/114":
			if r.Method == http.MethodDelete {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": ""})
				return
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client, err := proxmox.NewClient(proxmox.Config{
		BaseURL:  ts.URL,
		Node:     "pve",
		APIToken: "root@pam!test=1234",
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	client.SetDrainer(testNodeDrainer(func(_ context.Context, nodeName string) error {
		if nodeName != "talos-worker-3" {
			return fmt.Errorf("unexpected node name %q", nodeName)
		}
		return nil
	}))

	app := fiber.New()
	apiGroup := app.Group("/api")
	RegisterProxmoxRoutes(apiGroup, client)

	// 1. Test GET /api/proxmox/status
	t.Run("GET /api/proxmox/status", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/proxmox/status", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}
		var res map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&res)
		if res["configured"] != true {
			t.Errorf("Expected configured=true, got %v", res["configured"])
		}
		if res["node"] != "pve" {
			t.Errorf("Expected node=pve, got %v", res["node"])
		}
	})

	// 2. Test GET /api/proxmox/next-vmid
	t.Run("GET /api/proxmox/next-vmid", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/proxmox/next-vmid", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}
		var res map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&res)
		if res["vmid"].(float64) != 114 {
			t.Errorf("Expected vmid 114, got %v", res["vmid"])
		}
	})

	// 3. Test POST /api/proxmox/worker
	t.Run("POST /api/proxmox/worker", func(t *testing.T) {
		reqBody := `{"name": "talos-worker-3", "cores": 2, "memoryMB": 3072, "diskGB": 30}`
		req := httptest.NewRequest(http.MethodPost, "/api/proxmox/worker", strings.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req, 10000)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected 201 Created, got %d: %s", resp.StatusCode, string(body))
		}

		var res proxmox.CreateWorkerResult
		_ = json.NewDecoder(resp.Body).Decode(&res)
		if res.VMID != 114 {
			t.Errorf("Expected VMID 114, got %d", res.VMID)
		}
		if res.Name != "talos-worker-3" {
			t.Errorf("Expected name 'talos-worker-3', got %s", res.Name)
		}
		if res.Status != "running" {
			t.Errorf("Expected status 'running', got %s", res.Status)
		}
	})

	// 4. Test DELETE /api/proxmox/worker/:vmid
	t.Run("DELETE /api/proxmox/worker/114", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/proxmox/worker/114", nil)
		resp, err := app.Test(req, 10000)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected 200 OK, got %d: %s", resp.StatusCode, string(body))
		}

		var res map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&res)
		if res["success"] != true {
			t.Errorf("Expected success=true, got %v", res["success"])
		}
	})

	// 5. Test DELETE /api/proxmox/worker/110 (Forbidden: safety check on Control Plane)
	t.Run("DELETE /api/proxmox/worker/110 (Control Plane Protected)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/proxmox/worker/110", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		if resp.StatusCode != http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected 403 Forbidden for protected node, got %d: %s", resp.StatusCode, string(body))
		}
	})
}

package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetNodeStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check Auth header
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "PVEAPIToken=") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/api2/json/nodes/test-node/status":
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"cpu":    0.155,
					"uptime": 123456,
					"cpuinfo": map[string]interface{}{
						"model":   "Intel Xeon Test",
						"cores":   8,
						"cpus":    16,
						"sockets": 1,
					},
					"memory": map[string]interface{}{
						"total":     uint64(34359738368), // 32 GB
						"used":      uint64(17179869184), // 16 GB
						"free":      uint64(17179869184),
						"available": uint64(17179869184),
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		case "/api2/json/nodes/test-node/storage/local-lvm/status":
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"type":    "lvmthin",
					"total":   uint64(400000000000),
					"used":    uint64(100000000000),
					"avail":   uint64(300000000000),
					"active":  1,
					"enabled": 1,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client, err := NewClient(Config{
		BaseURL:        ts.URL,
		Node:           "test-node",
		APIToken:       "root@pam!test=12345678",
		DefaultStorage: "local-lvm",
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := client.GetNodeStatus(ctx)
	if err != nil {
		t.Fatalf("GetNodeStatus failed: %v", err)
	}

	if status.Node != "test-node" {
		t.Errorf("Expected node 'test-node', got %s", status.Node)
	}
	if status.CPUCores != 16 {
		t.Errorf("Expected 16 CPU cores, got %d", status.CPUCores)
	}
	if status.Memory.Total != 34359738368 {
		t.Errorf("Expected 34359738368 memory total, got %d", status.Memory.Total)
	}
	if status.Memory.UsagePercent != 50.0 {
		t.Errorf("Expected 50.0%% memory usage, got %f", status.Memory.UsagePercent)
	}
	if status.Storage.Free != 300000000000 {
		t.Errorf("Expected 300000000000 storage free, got %d", status.Storage.Free)
	}
	if status.Storage.UsagePercent != 25.0 {
		t.Errorf("Expected 25.0%% storage usage, got %f", status.Storage.UsagePercent)
	}
}

func TestGetNextVMID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/cluster/nextid" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": "115",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client, _ := NewClient(Config{
		BaseURL:  ts.URL,
		APIToken: "PVEAPIToken=root@pam!test=1234",
	})

	vmid, err := client.GetNextVMID(context.Background())
	if err != nil {
		t.Fatalf("GetNextVMID failed: %v", err)
	}
	if vmid != 115 {
		t.Errorf("Expected VMID 115, got %d", vmid)
	}
}

func TestCreateTalosWorker(t *testing.T) {
	var createdValues map[string]string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/api2/json/cluster/nextid":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": "120"})
		case path == "/api2/json/nodes/pve/qemu":
			if r.Method == http.MethodPost {
				_ = r.ParseForm()
				createdValues = make(map[string]string)
				for k, v := range r.PostForm {
					if len(v) > 0 {
						createdValues[k] = v[0]
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"data": "UPID:pve:00001:00001:00001:qmcreate:120:root@pam:",
				})
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		case strings.HasPrefix(path, "/api2/json/nodes/pve/tasks/"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"status":     "stopped",
					"exitstatus": "OK",
				},
			})
		case path == "/api2/json/nodes/pve/qemu/120/status/current":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"vmid":   120,
					"status": "running",
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client, _ := NewClient(Config{
		BaseURL:  ts.URL,
		Node:     "pve",
		APIToken: "root@pam!test=123",
	})

	res, err := client.CreateTalosWorker(context.Background(), CreateWorkerOpts{})
	if err != nil {
		t.Fatalf("CreateTalosWorker failed: %v", err)
	}

	if res.VMID != 120 {
		t.Errorf("Expected VMID 120, got %d", res.VMID)
	}
	if res.Status != "running" {
		t.Errorf("Expected status 'running', got %s", res.Status)
	}

	// Verify defaults
	if createdValues["cores"] != "2" {
		t.Errorf("Expected cores 2, got %s", createdValues["cores"])
	}
	if createdValues["memory"] != "3072" {
		t.Errorf("Expected memory 3072, got %s", createdValues["memory"])
	}
	if createdValues["scsi0"] != "local-lvm:30,ssd=1" {
		t.Errorf("Expected scsi0 'local-lvm:30,ssd=1', got %s", createdValues["scsi0"])
	}
	if createdValues["cdrom"] != "data:iso/talos-v1.14.0-qemu-guest-agent.iso" {
		t.Errorf("Expected cdrom 'data:iso/talos-v1.14.0-qemu-guest-agent.iso', got %s", createdValues["cdrom"])
	}
	if createdValues["net0"] != "virtio,bridge=vmbr0" {
		t.Errorf("Expected net0 'virtio,bridge=vmbr0', got %s", createdValues["net0"])
	}
	if createdValues["agent"] != "enabled=1" {
		t.Errorf("Expected agent 'enabled=1', got %s", createdValues["agent"])
	}
	if createdValues["start"] != "1" {
		t.Errorf("Expected start '1', got %s", createdValues["start"])
	}
}

func TestDeleteWorker(t *testing.T) {
	var stoppedCalled, deleteCalled bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/api2/json/nodes/pve/qemu/125/status/current":
			status := "running"
			if stoppedCalled {
				status = "stopped"
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"vmid":   125,
					"status": status,
				},
			})
		case path == "/api2/json/nodes/pve/qemu/125/status/stop":
			stoppedCalled = true
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": "UPID:pve:stop:125",
			})
		case strings.HasPrefix(path, "/api2/json/nodes/pve/tasks/"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"status":     "stopped",
					"exitstatus": "OK",
				},
			})
		case path == "/api2/json/nodes/pve/qemu/125":
			if r.Method == http.MethodDelete {
				deleteCalled = true
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"data": "",
				})
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client, _ := NewClient(Config{
		BaseURL:  ts.URL,
		Node:     "pve",
		APIToken: "root@pam!test=123",
	})

	err := client.DeleteWorker(context.Background(), 125)
	if err != nil {
		t.Fatalf("DeleteWorker failed: %v", err)
	}

	if !stoppedCalled {
		t.Error("Expected stop to be called before deletion")
	}
	if !deleteCalled {
		t.Error("Expected DELETE request to be issued")
	}
}

func TestTicketAuthentication(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/access/ticket" {
			_ = r.ParseForm()
			if r.PostFormValue("username") == "root@pam" && r.PostFormValue("password") == "secret123" {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"data": map[string]interface{}{
						"ticket":              "TICKET123",
						"CSRFPreventionToken": "CSRF123",
					},
				})
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if r.URL.Path == "/api2/json/cluster/nextid" {
			cookie, err := r.Cookie("PVEAuthCookie")
			if err != nil || cookie.Value != "TICKET123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": 121,
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	client, _ := NewClient(Config{
		BaseURL:  ts.URL,
		Username: "root@pam",
		Password: "secret123",
	})

	vmid, err := client.GetNextVMID(context.Background())
	if err != nil {
		t.Fatalf("GetNextVMID with ticket auth failed: %v", err)
	}
	if vmid != 121 {
		t.Errorf("Expected VMID 121, got %d", vmid)
	}
}

func TestLiveProxmoxHost(t *testing.T) {
	client := NewClientFromEnv()
	if !client.IsConfigured() {
		t.Skip("Live Proxmox client not configured, skipping live test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := client.GetNodeStatus(ctx)
	if err != nil {
		t.Fatalf("Live GetNodeStatus failed: %v", err)
	}

	t.Logf("Proxmox Node: %s (Uptime: %d s, CPU: %.2f%%, Cores: %d, Model: %s)",
		status.Node, status.Uptime, status.CPUUsagePercent, status.CPUCores, status.CPUModel)
	t.Logf("Memory: Total=%d, Used=%d, Free=%d, Usage=%.2f%%",
		status.Memory.Total, status.Memory.Used, status.Memory.Free, status.Memory.UsagePercent)
	t.Logf("Storage: %s Total=%d, Used=%d, Free=%d, Usage=%.2f%%",
		status.Storage.Name, status.Storage.Total, status.Storage.Used, status.Storage.Free, status.Storage.UsagePercent)

	nextID, err := client.GetNextVMID(ctx)
	if err != nil {
		t.Fatalf("Live GetNextVMID failed: %v", err)
	}
	t.Logf("Next available VMID: %d", nextID)

	if nextID <= 0 {
		t.Errorf("Expected next VMID > 0, got %d", nextID)
	}
}

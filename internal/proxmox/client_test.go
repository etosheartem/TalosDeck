package proxmox

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestAllocateVMID_Concurrency(t *testing.T) {
	// PVE-04: Test that concurrent worker creations never allocate duplicate VMIDs
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/cluster/nextid" {
			w.Header().Set("Content-Type", "application/json")
			// Proxmox returns 120 because it hasn't registered new VM yet
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": "120",
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

	const count = 10
	var wg sync.WaitGroup
	ids := make([]int, count)
	errs := make([]error, count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ids[idx], errs[idx] = client.allocateVMID(context.Background())
		}(i)
	}
	wg.Wait()

	seen := make(map[int]bool)
	for i, id := range ids {
		if errs[i] != nil {
			t.Fatalf("Error in allocateVMID goroutine %d: %v", i, errs[i])
		}
		if seen[id] {
			t.Errorf("Duplicate VMID detected in concurrent allocation: %d", id)
		}
		seen[id] = true
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
					"name":   "talos-worker-120",
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
	// PVE-06: ide2 with media=cdrom, cdrom CLI alias must be omitted
	if createdValues["ide2"] != "data:iso/talos-v1.14.0-qemu-guest-agent.iso,media=cdrom" {
		t.Errorf("Expected ide2 'data:iso/talos-v1.14.0-qemu-guest-agent.iso,media=cdrom', got %s", createdValues["ide2"])
	}
	if cdrom, ok := createdValues["cdrom"]; ok && cdrom != "" {
		t.Errorf("Expected cdrom parameter to be omitted from REST API call, got %s", cdrom)
	}
	// PVE-02: boot order prioritizing scsi0 before ide2
	if createdValues["boot"] != "order=scsi0;ide2" {
		t.Errorf("Expected boot 'order=scsi0;ide2', got %s", createdValues["boot"])
	}
	// PVE-09: ostype l26
	if createdValues["ostype"] != "l26" {
		t.Errorf("Expected ostype 'l26', got %s", createdValues["ostype"])
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

func TestCreateTalosWorker_Validation(t *testing.T) {
	client, _ := NewClient(Config{
		BaseURL:  "http://localhost",
		APIToken: "test",
	})

	// PVE-10: VMID < 100
	_, err := client.CreateTalosWorker(context.Background(), CreateWorkerOpts{
		VMID: 50,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid VMID") {
		t.Errorf("Expected invalid VMID error for VMID 50, got: %v", err)
	}

	// QEMU-04: Invalid RFC 1123 hostname
	invalidNames := []string{"TALOS_WORKER", "-invalid", "invalid-", "inv@lid"}
	for _, name := range invalidNames {
		_, err := client.CreateTalosWorker(context.Background(), CreateWorkerOpts{
			VMID: 150,
			Name: name,
		})
		if err == nil || !strings.Contains(err.Error(), "invalid VM name") {
			t.Errorf("Expected invalid VM name error for %q, got: %v", name, err)
		}
	}
}

func TestCreateTalosWorker_ReservesExplicitVMID(t *testing.T) {
	client, err := NewClient(Config{
		BaseURL:  "http://localhost",
		Node:     "pve",
		APIToken: "test",
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	if err := client.reserveVMID(150); err != nil {
		t.Fatalf("failed to arrange reservation: %v", err)
	}
	defer client.releaseVMID(150)

	_, err = client.CreateTalosWorker(context.Background(), CreateWorkerOpts{VMID: 150})
	if err == nil || !strings.Contains(err.Error(), "already being created") {
		t.Fatalf("expected duplicate explicit VMID to be rejected, got %v", err)
	}
}

func TestDeleteWorker_Success(t *testing.T) {
	var shutdownCalled, deleteCalled bool

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/api2/json/nodes/pve/qemu/125/status/current":
			status := "running"
			if shutdownCalled {
				status = "stopped"
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"vmid":   125,
					"name":   "talos-worker-125",
					"status": status,
				},
			})
		case path == "/api2/json/nodes/pve/qemu/125/status/shutdown":
			shutdownCalled = true
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": "UPID:pve:shutdown:125",
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
	drainCalled := false
	mockDrainer := &mockDrainerFunc{
		fn: func(ctx context.Context, nodeName string) error {
			if nodeName == "talos-worker-125" {
				drainCalled = true
			}
			return nil
		},
	}
	client.SetDrainer(mockDrainer)

	err := client.DeleteWorker(context.Background(), 125)
	if err != nil {
		t.Fatalf("DeleteWorker failed: %v", err)
	}

	if !drainCalled {
		t.Error("Expected CordonAndDrainNode to be called prior to VM shutdown and deletion (PVE-07)")
	}
	if !shutdownCalled {
		t.Error("Expected graceful ACPI shutdown to be called before deletion")
	}
	if !deleteCalled {
		t.Error("Expected DELETE request to be issued")
	}
}

type mockDrainerFunc struct {
	fn func(ctx context.Context, nodeName string) error
}

func (m *mockDrainerFunc) CordonAndDrainNode(ctx context.Context, nodeName string) error {
	return m.fn(ctx, nodeName)
}

func TestDeleteWorker_AbortsWhenDrainFails(t *testing.T) {
	shutdownCalled := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status/current"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{"vmid": 126, "name": "talos-worker-126", "status": "running"},
			})
		case strings.Contains(r.URL.Path, "/status/shutdown"), r.Method == http.MethodDelete:
			shutdownCalled = true
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": ""})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	client, err := NewClient(Config{BaseURL: ts.URL, Node: "pve", APIToken: "root@pam!test=123"})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	client.SetDrainer(&mockDrainerFunc{fn: func(context.Context, string) error {
		return errors.New("pod disruption budget blocked eviction")
	}})

	err = client.DeleteWorker(context.Background(), 126)
	if err == nil || !strings.Contains(err.Error(), "drain failed") {
		t.Fatalf("expected drain failure to abort deletion, got %v", err)
	}
	if shutdownCalled {
		t.Fatal("VM shutdown or deletion must not run after drain failure")
	}
}

func TestDeleteWorker_SafetyCheck_ProtectedVM(t *testing.T) {
	// PVE-01: Prohibit deleting Master (Control Plane) and host workstations
	protectedVMs := []struct {
		vmid int
		name string
	}{
		{110, "talos-cp-1"},
		{100, "talos-controlplane"},
		{101, "ubuntu-server"},
		{105, "win11"},
	}

	for _, tc := range protectedVMs {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/status/current") {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"data": map[string]interface{}{
						"vmid":   tc.vmid,
						"name":   tc.name,
						"status": "running",
					},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))

		client, _ := NewClient(Config{
			BaseURL:  ts.URL,
			Node:     "pve",
			APIToken: "root@pam!test=123",
		})

		err := client.DeleteWorker(context.Background(), tc.vmid)
		ts.Close()

		if err == nil {
			t.Errorf("Expected safety check error when deleting protected VM %s (ID %d), but got nil", tc.name, tc.vmid)
		} else if !strings.Contains(err.Error(), "safety check violation") {
			t.Errorf("Expected 'safety check violation' error message for %s, got: %v", tc.name, err)
		}
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

func TestTicketAuthentication_EmptyTicket(t *testing.T) {
	// PVE-11: Proxmox returns empty ticket on 2FA prompt
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/access/ticket" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"ticket":              "",
					"CSRFPreventionToken": "",
				},
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

	err := client.authenticate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "empty ticket") {
		t.Errorf("Expected empty ticket error, got: %v", err)
	}
}

func TestSessionRefreshOn401(t *testing.T) {
	// PVE-03: Session refresh on 401
	var authCount int
	var ticketVal = "TICKET_OLD"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/access/ticket" {
			authCount++
			ticketVal = "TICKET_NEW"
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"ticket":              ticketVal,
					"CSRFPreventionToken": "CSRF_NEW",
				},
			})
			return
		}

		if r.URL.Path == "/api2/json/cluster/nextid" {
			cookie, _ := r.Cookie("PVEAuthCookie")
			if cookie == nil || cookie.Value != "TICKET_NEW" {
				// Expired/invalid ticket
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": 122,
			})
			return
		}
	}))
	defer ts.Close()

	client, _ := NewClient(Config{
		BaseURL:  ts.URL,
		Username: "root@pam",
		Password: "secret123",
	})

	// Pre-populate old expired ticket
	client.ticket = "TICKET_OLD"
	client.ticketExpiresAt = time.Now().Add(10 * time.Minute)

	vmid, err := client.GetNextVMID(context.Background())
	if err != nil {
		t.Fatalf("Expected session re-auth and retry to succeed, got: %v", err)
	}
	if vmid != 122 {
		t.Errorf("Expected VMID 122, got %d", vmid)
	}
	if authCount != 1 {
		t.Errorf("Expected 1 re-authentication request, got %d", authCount)
	}
}

func TestPVE05_SecureByDefault(t *testing.T) {
	client := NewClientFromEnv()
	if client.cfg.SkipTLSVerify {
		t.Error("PVE-05: Expected SkipTLSVerify to default to false for security, but got true")
	}
}

func TestTLS_InvalidCustomCARejected(t *testing.T) {
	// PVE-05 / TLS-02: Invalid custom CA material must fail during setup.
	dummyCA := `-----BEGIN CERTIFICATE-----
MIIB/zCCAaagAwIBAgIUQW50aWdyYXZpdHlUZXN0Q0EwDQYJKoZIhvcNAQELBQAw
EjEQMA4GA1UEAwwHVGVzdCBDQTAeFw0yNjA5MTExMDAwMDBaFw0yNzA5MTExMDAw
MDBaMBIxEDAOBgNVBAMMB1Rlc3QgQ0EwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNC
AARb2V3h1234567890abcdefghijklmnopqrstuvwxyz01234567890abcdefghijkl
mnopqrstuvwxyz0123456789==
-----END CERTIFICATE-----`

	_, err := NewClient(Config{
		BaseURL: "https://pve.example.com:8006",
		CACert:  dummyCA,
	})
	if err == nil {
		t.Fatal("expected invalid custom CA to be rejected")
	}
}

func TestTLS_CustomCA(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/cluster/nextid" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": 123})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw})
	client, err := NewClient(Config{
		BaseURL:  ts.URL,
		APIToken: "root@pam!test=123",
		CACert:   string(caPEM),
	})
	if err != nil {
		t.Fatalf("failed to configure custom CA: %v", err)
	}
	vmid, err := client.GetNextVMID(context.Background())
	if err != nil {
		t.Fatalf("request with custom CA failed: %v", err)
	}
	if vmid != 123 {
		t.Fatalf("expected VMID 123, got %d", vmid)
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
		if strings.Contains(err.Error(), "certificate") {
			t.Skipf("Live Proxmox test skipped (self-signed lab certificate, set PROXMOX_SKIP_TLS_VERIFY=true to run live): %v", err)
		}
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

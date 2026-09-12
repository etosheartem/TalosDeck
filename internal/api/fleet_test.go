package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wsclient "github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v2"
	fiberws "github.com/gofiber/websocket/v2"
	"github.com/google/uuid"

	"talosdeck/internal/alerts"
	"talosdeck/internal/audit"
	"talosdeck/internal/auth"
	"talosdeck/internal/clusters"
	"talosdeck/internal/proxmox"
)

type fleetFixture struct {
	fleet *Fleet
	app   *fiber.App
	auth  *auth.AuthManager
	audit *audit.AuditManager
	token string
}

func newFleetFixture(t *testing.T) fleetFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := clusters.Open(filepath.Join(dir, "data", "clusters.db"), filepath.Join(dir, "keys", "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	am := auth.NewAuthManager("fleet-test-password", "fleet-test-signing-key-at-least-32-bytes")
	token, err := am.GenerateToken("admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	f, err := OpenFleet(FleetOptions{Store: store, Auth: am, DataDir: filepath.Join(dir, "data")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	aj, err := audit.NewAuditManager(filepath.Join(dir, "audit.log"), 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { aj.Close() })
	app := SetupServer(ServerConfig{Fleet: f, Auth: am, Audit: aj, AlertService: alerts.NewTelegramService("", "", false), Proxmox: &proxmox.Client{}})
	t.Cleanup(func() { _ = app.Shutdown() })
	return fleetFixture{f, app, am, aj, token}
}

func fleetRequest(t *testing.T, app *fiber.App, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := app.Test(req, 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, raw
}

func TestFleetEmptyRegistryAuthAndImportRedaction(t *testing.T) {
	f := newFleetFixture(t)
	if code, _ := fleetRequest(t, f.app, "GET", "/api/clusters", "", nil); code != 401 {
		t.Fatalf("anonymous registry status=%d", code)
	}
	if code, raw := fleetRequest(t, f.app, "GET", "/api/clusters", f.token, nil); code != 200 || string(raw) != `{"clusters":[]}` {
		t.Fatalf("empty registry %d %s", code, raw)
	}
	if code, _ := fleetRequest(t, f.app, "GET", "/readyz", "", nil); code != 200 {
		t.Fatalf("zero-cluster readiness=%d", code)
	}
	code, raw := fleetRequest(t, f.app, "POST", "/api/auth/login", "", map[string]string{"password": "fleet-test-password"})
	if code != 200 {
		t.Fatalf("login blocked without clusters: %d %s", code, raw)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &login); err != nil || login.Token == "" {
		t.Fatalf("login token unavailable: %s", raw)
	}
	if code, _ = fleetRequest(t, f.app, "GET", "/api/auth/me", login.Token, nil); code != 200 {
		t.Fatalf("global auth me=%d", code)
	}
	if code, _ = fleetRequest(t, f.app, "GET", "/api/nodes", login.Token, nil); code != 409 {
		t.Fatalf("legacy endpoint silently selected cluster: %d", code)
	}
	viewer, err := f.auth.GenerateToken("viewer", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	request := ImportClusterRequest{Name: "test", Talosconfig: "context: SECRET-IMPORT-SENTINEL\ncontexts: [invalid", Kubeconfig: "KUBE-SECRET-SENTINEL"}
	if code, _ = fleetRequest(t, f.app, "POST", "/api/clusters", viewer, request); code != 403 {
		t.Fatalf("viewer import status=%d", code)
	}
	code, raw = fleetRequest(t, f.app, "POST", "/api/clusters", f.token, request)
	if code != 400 {
		t.Fatalf("invalid import status=%d %s", code, raw)
	}
	for _, secret := range []string{"SECRET-IMPORT-SENTINEL", "KUBE-SECRET-SENTINEL"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("import error leaked submitted credentials: %s", raw)
		}
	}
	list, err := f.fleet.options.Store.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("failed import persisted record: %+v %v", list, err)
	}
}

func TestFleetDispatchIsolatesIdenticalNodesJobsAndMutations(t *testing.T) {
	f := newFleetFixture(t)
	ids := []string{uuid.NewString(), uuid.NewString()}
	for _, id := range ids {
		id := id
		child := fiber.New()
		child.All("/api/*", func(c *fiber.Ctx) error {
			return c.JSON(fiber.Map{"clusterId": id, "path": c.Path(), "query": c.OriginalURL(), "method": c.Method(), "body": string(c.Body())})
		})
		f.fleet.runtimes[id] = &clusterRuntime{handler: child.Handler()}
	}
	f.fleet.legacyID = ids[0]
	for _, id := range ids {
		for _, suffix := range []string{"nodes/10.0.0.1", "jobs/shared-job", "jobs/shared-job/export", "config/10.0.0.1/history", "backups/same-backup/download", "audit?node=same-node"} {
			code, raw := fleetRequest(t, f.app, "GET", "/api/clusters/"+id+"/"+suffix, f.token, nil)
			var result map[string]string
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
			if code != 200 || result["clusterId"] != id || result["path"] != "/api/"+strings.Split(suffix, "?")[0] {
				t.Fatalf("wrong scoped dispatch: %d %s", code, raw)
			}
			if strings.Contains(suffix, "?") && !strings.Contains(result["query"], "node=same-node") {
				t.Fatalf("query lost: %s", raw)
			}
		}
		payload := map[string]string{"planId": "same-plan", "confirmedNode": "10.0.0.1"}
		code, raw := fleetRequest(t, f.app, "POST", "/api/clusters/"+id+"/config/10.0.0.1/apply", f.token, payload)
		if code != 200 || !bytes.Contains(raw, []byte(id)) || !bytes.Contains(raw, []byte("same-plan")) {
			t.Fatalf("mutation dispatch lost body or scope: %d %s", code, raw)
		}
		for _, suffix := range []string{"nodes/10.0.0.1", "jobs/shared-job", "config/10.0.0.1/apply"} {
			if code, _ := fleetRequest(t, f.app, "POST", "/api/clusters/"+id+"/"+suffix, "", payload); code != 401 {
				t.Fatalf("auth bypass %s: %d", suffix, code)
			}
		}
		if code, _ := fleetRequest(t, f.app, "GET", "/api/clusters/"+id+"/auth/me", f.token, nil); code != 404 {
			t.Fatalf("scoped auth must remain global: %d", code)
		}
	}
	for _, unknown := range []string{uuid.NewString(), "invalid-id"} {
		if code, raw := fleetRequest(t, f.app, "GET", "/api/clusters/"+unknown+"/nodes", f.token, nil); code != 404 {
			t.Fatalf("unknown cluster fell back to legacy: %d %s", code, raw)
		}
	}
	if code, raw := fleetRequest(t, f.app, "GET", "/api/nodes", f.token, nil); code != 200 || !bytes.Contains(raw, []byte(ids[0])) {
		t.Fatalf("legacy mapping lost: %d %s", code, raw)
	}
	if code, _ := fleetRequest(t, f.app, "GET", "/api/nodes", "", nil); code != 401 {
		t.Fatalf("legacy auth bypass=%d", code)
	}
}

func serveFleet(t *testing.T, app *fiber.App) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- app.Listener(listener) }()
	t.Cleanup(func() {
		_ = app.Shutdown()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("fleet listener did not stop")
		}
	})
	return "ws://" + listener.Addr().String()
}

func TestFleetWebSocketDispatchAndRealChildAuthentication(t *testing.T) {
	f := newFleetFixture(t)
	ids := []string{uuid.NewString(), uuid.NewString()}
	for _, id := range ids {
		id := id
		child := fiber.New()
		child.Get("/ws/nodes/:ip/logs/:service", fiberws.New(func(c *fiberws.Conn) {
			defer c.Close()
			_ = c.WriteMessage(fiberws.TextMessage, []byte(id+"/"+c.Params("ip")+"/"+c.Params("service")))
		}))
		f.fleet.runtimes[id] = &clusterRuntime{handler: child.Handler()}
	}
	// This runtime uses the production WebSocket authentication middleware.
	// An invalid IP exits safely before invoking the external Talos SDK.
	authID := uuid.NewString()
	realChild := SetupServer(ServerConfig{Auth: f.auth, Audit: f.audit, AlertService: alerts.NewTelegramService("", "", false), Proxmox: &proxmox.Client{}})
	f.fleet.runtimes[authID] = &clusterRuntime{handler: realChild.Handler()}
	address := serveFleet(t, f.app)
	for _, id := range ids {
		conn, res, err := wsclient.DefaultDialer.Dial(address+"/api/clusters/"+id+"/ws/nodes/10.0.0.1/logs/kubelet", nil)
		if err != nil {
			if res != nil {
				t.Fatalf("scoped WS status=%d: %v", res.StatusCode, err)
			}
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, message, err := conn.ReadMessage()
		_ = conn.Close()
		if err != nil || string(message) != id+"/10.0.0.1/kubelet" {
			t.Fatalf("cross-cluster stream: %q %v", message, err)
		}
	}
	path := address + "/api/clusters/" + authID + "/ws/nodes/invalid-ip/dmesg"
	conn, res, err := wsclient.DefaultDialer.Dial(path, nil)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous websocket bypass: response=%v err=%v", res, err)
	}
	dialer := wsclient.Dialer{Subprotocols: []string{f.token}, HandshakeTimeout: 3 * time.Second}
	conn, res, err = dialer.Dial(path, nil)
	if err != nil {
		t.Fatalf("subprotocol authentication failed: response=%v err=%v", res, err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, message, err := conn.ReadMessage()
	_ = conn.Close()
	if err != nil || !strings.Contains(string(message), "invalid node IP") {
		t.Fatalf("wrong real-child WS dispatch: %q %v", message, err)
	}
}

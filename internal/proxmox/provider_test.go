package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testMachine() MachineSpec {
	return MachineSpec{Name: "worker-1", Role: "worker", Cores: 2, MemoryMB: 2048, DiskGB: 20, Storage: "local-lvm", ISO: "local:iso/talos.iso", Bridge: "vmbr0", NetworkMode: "dhcp"}
}
func TestProviderOwnershipAndGuestMACBinding(t *testing.T) {
	spec := testMachine()
	record := NewOwnership("provider", "cluster", "plan", "pve", spec, 150)
	owned := record.Machine()
	config := map[string]string{"name": owned.Name, "description": ownershipMarker(owned), "smbios1": "uuid=" + owned.ID, "net0": "virtio=" + owned.MAC + ",bridge=vmbr0"}
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/config"):
			json.NewEncoder(w).Encode(map[string]any{"data": config})
		case strings.HasSuffix(r.URL.Path, "network-get-interfaces"):
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"result": []any{map[string]any{"hardware-address": "aa:bb:cc:dd:ee:ff", "ip-addresses": []any{map[string]any{"ip-address": "10.0.0.99"}}}, map[string]any{"hardware-address": owned.MAC, "ip-addresses": []any{map[string]any{"ip-address": "127.0.0.1"}, map[string]any{"ip-address": "10.0.0.15"}}}}}})
		default:
			mutations++
			json.NewEncoder(w).Encode(map[string]any{"data": "task"})
		}
	}))
	defer server.Close()
	c, err := NewClient(Config{BaseURL: server.URL, Node: "pve", APIToken: "root@pam!test=value"})
	if err != nil {
		t.Fatal(err)
	}
	address, err := c.MachineAddress(context.Background(), owned)
	if err != nil || address != "10.0.0.15" {
		t.Fatalf("address=%s err=%v", address, err)
	}
	config["description"] = "another administrator's VM"
	if err := c.DeleteOwned(context.Background(), owned); err == nil {
		t.Fatal("foreign VM deletion accepted")
	}
	if mutations != 0 {
		t.Fatal("ownership rejection mutated provider")
	}
}
func TestCreateMachineUsesPersistedIdentityAndRejectsParameterInjection(t *testing.T) {
	spec := testMachine()
	spec.VLAN = 42
	record := NewOwnership("provider", "cluster", "plan", "pve", spec, 151)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.ParseForm()
		if r.Form.Get("description") != ownershipMarker(record.Machine()) || r.Form.Get("smbios1") != "uuid="+record.ID || r.Form.Get("start") != "0" || !strings.Contains(r.Form.Get("net0"), ",tag=42") {
			t.Error("creation did not bind ownership and network")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": "UPID:test"})
	}))
	defer server.Close()
	c, _ := NewClient(Config{BaseURL: server.URL, Node: "pve", APIToken: "root@pam!test=value"})
	if _, err := c.CreateMachine(context.Background(), spec, record.Machine()); err != nil {
		t.Fatal(err)
	}
	spec.Bridge = "vmbr0,firewall=0"
	if _, err := c.CreateMachine(context.Background(), spec, record.Machine()); err == nil {
		t.Fatal("provider parameter injection accepted")
	}
	if calls != 1 {
		t.Fatalf("unexpected provider writes: %d", calls)
	}
}

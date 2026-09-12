package proxmox

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MachineProvider is the provider boundary used by lifecycle jobs. A provider
// must verify the persistent ownership record before operating on an existing VM.
type MachineProvider interface {
	NextID(context.Context) (int, error)
	CreateMachine(context.Context, MachineSpec, OwnedMachine) (string, error)
	WaitTask(context.Context, string) error
	VerifyOwned(context.Context, OwnedMachine) error
	StartOwned(context.Context, OwnedMachine) error
	MachineAddress(context.Context, OwnedMachine) (string, error)
	DeleteOwned(context.Context, OwnedMachine) error
}
type MachineSpec struct {
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Cores       int      `json:"cores"`
	MemoryMB    int      `json:"memoryMB"`
	DiskGB      int      `json:"diskGB"`
	Storage     string   `json:"storage,omitempty"`
	ISO         string   `json:"iso,omitempty"`
	Bridge      string   `json:"bridge,omitempty"`
	VLAN        int      `json:"vlan,omitempty"`
	NetworkMode string   `json:"networkMode"`
	Address     string   `json:"address,omitempty"`
	Gateway     string   `json:"gateway,omitempty"`
	Nameservers []string `json:"nameservers,omitempty"`
}
type OwnedMachine struct {
	ID              string    `json:"id"`
	ProviderID      string    `json:"providerId"`
	ClusterID       string    `json:"clusterId"`
	PlanID          string    `json:"planId"`
	Name            string    `json:"name"`
	Role            string    `json:"role"`
	VMID            int       `json:"vmid"`
	ProviderNode    string    `json:"providerNode"`
	MAC             string    `json:"mac"`
	Address         string    `json:"address,omitempty"`
	Status          string    `json:"status"`
	CleanupEligible bool      `json:"cleanupEligible"`
	CreatedAt       time.Time `json:"createdAt"`
	OwnershipToken  string    `json:"-"`
}

// OwnedMachineRecord's token is stored only inside encrypted lifecycle state.
type OwnedMachineRecord struct {
	OwnedMachine
	Token string `json:"ownershipToken"`
}

func NewOwnership(providerID, clusterID, planID, node string, spec MachineSpec, vmid int) OwnedMachineRecord {
	mac := make([]byte, 6)
	if _, err := rand.Read(mac); err != nil {
		panic("cannot generate machine identity")
	}
	mac[0] = (mac[0] | 2) & 0xfe
	token := uuid.NewString()
	return OwnedMachineRecord{OwnedMachine: OwnedMachine{ID: uuid.NewString(), ProviderID: providerID, ClusterID: clusterID, PlanID: planID, Name: spec.Name, Role: spec.Role, VMID: vmid, ProviderNode: node, MAC: net.HardwareAddr(mac).String(), Status: "reserved", CreatedAt: time.Now().UTC(), OwnershipToken: token}, Token: token}
}
func (r OwnedMachineRecord) Machine() OwnedMachine {
	m := r.OwnedMachine
	m.OwnershipToken = r.Token
	return m
}

var resourceName = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
var isoVolume = regexp.MustCompile(`^[a-zA-Z0-9_.-]+:iso/[a-zA-Z0-9_.-]+\.iso$`)

func ValidateMachine(spec MachineSpec) error {
	if !isValidNodeName(spec.Name) {
		return fmt.Errorf("machine name must be a DNS label")
	}
	if spec.Role != "controlplane" && spec.Role != "worker" {
		return fmt.Errorf("machine role must be controlplane or worker")
	}
	if spec.Cores < 1 || spec.Cores > 128 || spec.MemoryMB < 1024 || spec.MemoryMB > 1048576 || spec.DiskGB < 8 || spec.DiskGB > 65536 {
		return fmt.Errorf("machine resources outside supported range")
	}
	if spec.Storage != "" && !resourceName.MatchString(spec.Storage) {
		return fmt.Errorf("invalid storage name")
	}
	if spec.Bridge != "" && !resourceName.MatchString(spec.Bridge) {
		return fmt.Errorf("invalid bridge name")
	}
	if spec.ISO != "" && !isoVolume.MatchString(spec.ISO) {
		return fmt.Errorf("ISO must be a Proxmox storage:iso/file.iso volume")
	}
	if spec.VLAN < 0 || spec.VLAN > 4094 {
		return fmt.Errorf("VLAN must be between 0 and 4094")
	}
	if spec.NetworkMode != "dhcp" && spec.NetworkMode != "static" {
		return fmt.Errorf("networkMode must be dhcp or static")
	}
	if spec.NetworkMode == "static" {
		address, err := netip.ParsePrefix(spec.Address)
		if err != nil || !address.Addr().Is4() || !address.Addr().IsGlobalUnicast() {
			return fmt.Errorf("static address must be a routable CIDR")
		}
		gateway, err := netip.ParseAddr(spec.Gateway)
		if err != nil || !address.Contains(gateway) {
			return fmt.Errorf("gateway must be in the static subnet")
		}
		if len(spec.Nameservers) == 0 {
			return fmt.Errorf("static networking requires a nameserver")
		}
	} else if spec.Address != "" || spec.Gateway != "" {
		return fmt.Errorf("DHCP must not include a static address or gateway")
	}
	for _, server := range spec.Nameservers {
		if _, err := netip.ParseAddr(server); err != nil {
			return fmt.Errorf("invalid nameserver")
		}
	}
	return nil
}
func (c *Client) NextID(ctx context.Context) (int, error) { return c.GetNextVMID(ctx) }
func ownershipMarker(m OwnedMachine) string {
	return "talosdeck-owner=" + m.OwnershipToken + ";machine=" + m.ID
}
func (c *Client) CreateMachine(ctx context.Context, spec MachineSpec, m OwnedMachine) (string, error) {
	if err := ValidateMachine(spec); err != nil {
		return "", err
	}
	if _, err := uuid.Parse(m.ID); err != nil {
		return "", fmt.Errorf("invalid machine identity")
	}
	if _, err := uuid.Parse(m.OwnershipToken); err != nil {
		return "", fmt.Errorf("invalid ownership token")
	}
	if m.VMID < 100 || m.Name != spec.Name || m.ProviderNode != c.cfg.Node {
		return "", fmt.Errorf("invalid provider ownership reservation")
	}
	if _, err := net.ParseMAC(m.MAC); err != nil {
		return "", fmt.Errorf("invalid reserved MAC")
	}
	if spec.Storage == "" {
		spec.Storage = c.cfg.DefaultStorage
	}
	if spec.ISO == "" {
		spec.ISO = c.cfg.DefaultISO
	}
	if spec.Bridge == "" {
		spec.Bridge = c.cfg.DefaultBridge
	}
	if err := ValidateMachine(spec); err != nil {
		return "", err
	}
	form := url.Values{"vmid": {strconv.Itoa(m.VMID)}, "name": {spec.Name}, "cores": {strconv.Itoa(spec.Cores)}, "sockets": {"1"}, "cpu": {"host"}, "ostype": {"l26"}, "memory": {strconv.Itoa(spec.MemoryMB)}, "scsihw": {"virtio-scsi-single"}, "scsi0": {fmt.Sprintf("%s:%d,ssd=1", spec.Storage, spec.DiskGB)}, "ide2": {spec.ISO + ",media=cdrom"}, "boot": {"order=scsi0;ide2"}, "agent": {"enabled=1"}, "onboot": {"1"}, "start": {"0"}, "description": {ownershipMarker(m)}, "smbios1": {"uuid=" + m.ID}, "tags": {"talosdeck"}}
	network := "virtio=" + m.MAC + ",bridge=" + spec.Bridge
	if spec.VLAN > 0 {
		network += ",tag=" + strconv.Itoa(spec.VLAN)
	}
	form.Set("net0", network)
	var response struct {
		Data string `json:"data"`
	}
	if err := c.postForm(ctx, "/nodes/"+url.PathEscape(c.cfg.Node)+"/qemu", form, &response); err != nil {
		return "", fmt.Errorf("Proxmox VM creation was not confirmed")
	}
	if response.Data == "" {
		return "", fmt.Errorf("Proxmox returned no creation task")
	}
	return response.Data, nil
}
func (c *Client) WaitTask(ctx context.Context, task string) error {
	return c.WaitForTask(ctx, task, 5*time.Minute)
}
func (c *Client) VerifyOwned(ctx context.Context, m OwnedMachine) error {
	if m.VMID < 100 || m.ProviderNode != c.cfg.Node || m.OwnershipToken == "" || m.ID == "" {
		return fmt.Errorf("machine ownership is incomplete")
	}
	var config struct {
		Data struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			SMBIOS      string `json:"smbios1"`
			Network     string `json:"net0"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/config", url.PathEscape(c.cfg.Node), m.VMID), &config); err != nil {
		return fmt.Errorf("cannot verify provider machine ownership")
	}
	if config.Data.Name != m.Name || config.Data.Description != ownershipMarker(m) || !strings.Contains(config.Data.SMBIOS, "uuid="+m.ID) || !strings.Contains(strings.ToLower(config.Data.Network), strings.ToLower(m.MAC)) {
		return fmt.Errorf("provider VM does not match its registered ownership record")
	}
	return nil
}
func (c *Client) StartOwned(ctx context.Context, m OwnedMachine) error {
	if err := c.VerifyOwned(ctx, m); err != nil {
		return err
	}
	return c.StartVM(ctx, m.VMID)
}
func (c *Client) MachineAddress(ctx context.Context, m OwnedMachine) (string, error) {
	if err := c.VerifyOwned(ctx, m); err != nil {
		return "", err
	}
	var response struct {
		Data struct {
			Result []struct {
				MAC       string `json:"hardware-address"`
				Addresses []struct {
					IP string `json:"ip-address"`
				} `json:"ip-addresses"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/agent/network-get-interfaces", url.PathEscape(c.cfg.Node), m.VMID), &response); err != nil {
		return "", fmt.Errorf("guest agent network addresses unavailable")
	}
	for _, iface := range response.Data.Result {
		if !strings.EqualFold(iface.MAC, m.MAC) {
			continue
		}
		for _, a := range iface.Addresses {
			if ip, err := netip.ParseAddr(a.IP); err == nil && ip.Is4() && ip.IsGlobalUnicast() && !ip.IsLoopback() {
				return ip.String(), nil
			}
		}
	}
	return "", fmt.Errorf("guest agent has no routable IPv4 for the registered MAC")
}
func (c *Client) DeleteOwned(ctx context.Context, m OwnedMachine) error {
	if err := c.VerifyOwned(ctx, m); err != nil {
		return err
	}
	status, err := c.GetVMStatus(ctx, m.VMID)
	if err != nil {
		return fmt.Errorf("cannot read owned VM status")
	}
	if status.Status == "running" {
		if err := c.StopVM(ctx, m.VMID); err != nil {
			return fmt.Errorf("owned VM stop failed")
		}
	}
	// Recheck after stopping: a reused VMID or edited ownership marker must never
	// allow deletion of another administrator's machine.
	if err := c.VerifyOwned(ctx, m); err != nil {
		return err
	}
	var response struct {
		Data string `json:"data"`
	}
	if err := c.deleteJSON(ctx, fmt.Sprintf("/nodes/%s/qemu/%d?purge=1", url.PathEscape(c.cfg.Node), m.VMID), &response); err != nil {
		return fmt.Errorf("owned VM deletion was not confirmed")
	}
	if response.Data == "" {
		return fmt.Errorf("Proxmox returned no deletion task")
	}
	return c.WaitTask(ctx, response.Data)
}

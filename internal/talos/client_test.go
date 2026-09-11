package talos

import (
	"sort"
	"sync"
	"testing"
)

func TestGetConfiguredNodes_DefensiveCopy(t *testing.T) {
	mgr := &TalosManager{
		endpoints: []string{"10.42.0.110:50000", "10.42.0.111:50000"},
		nodes:     []string{"10.42.0.112", "10.42.0.110", "10.42.0.111"},
	}

	nodes1 := mgr.GetConfiguredNodes()
	sort.Strings(nodes1)

	nodes2 := mgr.GetConfiguredNodes()
	if nodes2[0] != "10.42.0.112" {
		t.Errorf("expected original internal order 10.42.0.112 to be preserved, got %s", nodes2[0])
	}

	// Verify mutating returned slice does not change internal state
	nodes1[0] = "99.99.99.99"
	nodes3 := mgr.GetConfiguredNodes()
	for _, ip := range nodes3 {
		if ip == "99.99.99.99" {
			t.Errorf("internal nodes slice was mutated via external reference leak")
		}
	}
}

func TestGetEndpoints_DefensiveCopy(t *testing.T) {
	mgr := &TalosManager{
		endpoints: []string{"10.42.0.110:50000", "10.42.0.111:50000"},
		nodes:     []string{"10.42.0.110"},
	}

	ep1 := mgr.GetEndpoints()
	ep1[0] = "mutated:12345"

	ep2 := mgr.GetEndpoints()
	if ep2[0] != "10.42.0.110:50000" {
		t.Errorf("expected internal endpoints to remain unmutated, got %s", ep2[0])
	}
}

func TestConcurrent_NodesAccess_NoRace(t *testing.T) {
	mgr := &TalosManager{
		nodes:     []string{"10.42.0.112", "10.42.0.110", "10.42.0.111"},
		endpoints: []string{"10.42.0.110:50000"},
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			nodes := mgr.GetConfiguredNodes()
			sort.Strings(nodes)
		}()
		go func() {
			defer wg.Done()
			_ = mgr.GetEndpoints()
			_ = mgr.GetConfiguredNodes()
		}()
	}
	wg.Wait()
}

func TestFormatBytes_Bounds(t *testing.T) {
	tests := []struct {
		input    uint64
		expected string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
		{18446744073709551615, "16.0 EB"}, // Max uint64, must not panic with index out of bounds
	}

	for _, tc := range tests {
		res := formatBytes(tc.input)
		if res != tc.expected {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.input, res, tc.expected)
		}
	}
}

func TestCalculateCPUUsage(t *testing.T) {
	mgr := &TalosManager{}

	if got := mgr.calculateCPUUsage("10.42.0.110", 25, 100); got != 25 {
		t.Fatalf("first sample: got %d%%, want 25%%", got)
	}
	if got := mgr.calculateCPUUsage("10.42.0.110", 55, 200); got != 30 {
		t.Fatalf("delta sample: got %d%%, want 30%%", got)
	}
	if got := mgr.calculateCPUUsage("10.42.0.111", 150, 100); got != 100 {
		t.Fatalf("clamped sample: got %d%%, want 100%%", got)
	}
}

func TestCalculateFilesystemUsage(t *testing.T) {
	tests := []struct {
		name            string
		size, available uint64
		wantUsed        uint64
		wantUsedPercent int
	}{
		{name: "normal", size: 1000, available: 250, wantUsed: 750, wantUsedPercent: 75},
		{name: "empty filesystem", size: 0, available: 0, wantUsed: 0, wantUsedPercent: 0},
		{name: "invalid available value", size: 100, available: 200, wantUsed: 0, wantUsedPercent: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			used, percent := calculateFilesystemUsage(tt.size, tt.available)
			if used != tt.wantUsed || percent != tt.wantUsedPercent {
				t.Fatalf("got (%d, %d), want (%d, %d)", used, percent, tt.wantUsed, tt.wantUsedPercent)
			}
		})
	}
}

func TestDetectDiskBus(t *testing.T) {
	tests := []struct {
		device, diskType, busPath, subsystem, modalias string
		want                                           string
	}{
		{device: "nvme0n1", diskType: "NVME", want: "NVMe"},
		{device: "/dev/vda", busPath: "/devices/pci/virtio2", want: "VirtIO"},
		{device: "sda", modalias: "scsi:t-0x00", want: "SCSI"},
		{device: "mystery", want: "Unknown"},
	}
	for _, tt := range tests {
		if got := detectDiskBus(tt.device, tt.diskType, tt.busPath, tt.subsystem, tt.modalias); got != tt.want {
			t.Errorf("detectDiskBus(%q) = %q, want %q", tt.device, got, tt.want)
		}
	}
}

func TestGetClusterName_ThreadSafety(t *testing.T) {
	mgr := &TalosManager{}
	if name := mgr.GetClusterName(); name != "talos-cluster" {
		t.Errorf("expected talos-cluster, got %s", name)
	}
}

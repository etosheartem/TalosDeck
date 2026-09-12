package talos

import "testing"

func TestUpgradeDiskReserveRejectsFullOrUnknownUsage(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	for _, tt := range []struct {
		size, free uint64
		ok         bool
	}{{20 * gib, 4 * gib, true}, {20 * gib, gib, false}, {4 * gib, gib / 2, false}, {0, 0, false}, {gib, 2 * gib, false}} {
		if err := validateUpgradeReserve(tt.size, tt.free); (err == nil) != tt.ok {
			t.Fatalf("size=%d free=%d: %v", tt.size, tt.free, err)
		}
	}
}

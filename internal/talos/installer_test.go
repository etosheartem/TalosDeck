package talos

import (
	"context"
	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"github.com/siderolabs/talos/pkg/machinery/resources/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLegacySchematicFallbackDoesNotBypassAuthorizationErrors(t *testing.T) {
	for _, test := range []struct {
		err     error
		allowed bool
	}{
		{status.Error(codes.PermissionDenied, `resource type "ImageFactorySchematics.runtime.talos.dev" is not supported`), true},
		{status.Error(codes.PermissionDenied, "access denied"), false},
		{status.Error(codes.PermissionDenied, `resource type "ExtensionStatuses.runtime.talos.dev" is not supported`), false},
		{status.Error(codes.Unavailable, "connection lost"), false},
		{context.DeadlineExceeded, false},
	} {
		if got := schematicResourceUnavailable(test.err); got != test.allowed {
			t.Fatalf("fallback=%v for %v", got, test.err)
		}
	}
}

func TestLiveInstallerSchematic(t *testing.T) {
	path := os.Getenv("TALOSDECK_TEST_TALOSCONFIG")
	if path == "" {
		t.Skip("set TALOSDECK_TEST_TALOSCONFIG for read-only integration")
	}
	mgr, err := NewTalosManager(path)
	if err != nil {
		t.Fatal("cannot create Talos client")
	}
	defer mgr.Close()
	targets := mgr.GetConfiguredNodes()
	if len(targets) == 0 {
		t.Fatal("no configured nodes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nodeCtx := client.WithNode(ctx, targets[0])
	_, err = safe.StateGet[*runtime.ImageFactorySchematic](nodeCtx, mgr.GetClient().COSI, resource.NewMetadata(runtime.NamespaceName, runtime.ImageFactorySchematicType, runtime.ImageFactorySchematicID, resource.VersionUndefined))
	t.Logf("schematic lookup: code=%s error=%v", status.Code(err), err)
	extensions, err := safe.StateListAll[*runtime.ExtensionStatus](nodeCtx, mgr.GetClient().COSI)
	if err != nil {
		t.Fatal(err)
	}
	for extension := range extensions.All() {
		t.Logf("extension name=%s version=%s", extension.TypedSpec().Metadata.Name, extension.TypedSpec().Metadata.Version)
	}
	image, err := mgr.GetInstallerImage(ctx, targets[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("verified installer image: %s", image)
}

func TestInstallerMatchesRuntimeSchematic(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, test := range []struct {
		name, image, schematic string
		extensions             []string
		allowed                bool
	}{
		{"same schematic", "factory.talos.dev/metal-installer/" + id + ":v1.13.0", id, []string{"qemu-guest-agent"}, true},
		{"schematic mirror", "registry.local/talos/" + id + ":v1.13.0", id, []string{"iscsi-tools"}, true},
		{"changed schematic", "factory.talos.dev/metal-installer/" + strings.Repeat("b", 64) + ":v1.13.0", id, []string{"iscsi-tools"}, false},
		{"plain strips extensions", "ghcr.io/siderolabs/installer:v1.13.0", id, []string{"qemu-guest-agent"}, false},
		{"unknown custom extensions", "ghcr.io/siderolabs/installer:v1.13.0", "", []string{"custom-driver"}, false},
		{"unverified factory", "factory.talos.dev/metal-installer/" + id + ":v1.13.0", "", nil, false},
		{"plain no extensions", "ghcr.io/siderolabs/installer:v1.13.0", "", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateInstallerRuntime(test.image, test.schematic, test.extensions)
			if (err == nil) != test.allowed {
				t.Fatalf("allowed=%v: %v", test.allowed, err)
			}
		})
	}
}

func TestLiveNodeImageInventory(t *testing.T) {
	path := os.Getenv("TALOSDECK_TEST_TALOSCONFIG")
	if path == "" {
		t.Skip("opt-in read-only Talos inventory")
	}
	mgr, err := NewTalosManager(path)
	if err != nil {
		t.Fatal("cannot create Talos client")
	}
	defer mgr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	nodes, err := mgr.ListNodes(ctx)
	if err != nil || len(nodes) == 0 {
		t.Fatal("node inventory unavailable")
	}
	for _, node := range nodes {
		report, err := mgr.GetNodeImageStatus(ctx, node.IP)
		if err != nil {
			t.Fatalf("%s: %v", node.IP, err)
		}
		t.Logf("node=%s schematic=%s consistent=%t extensions=%v", node.IP, report.SchematicID, report.Consistent, report.Extensions)
		if report.CheckedAt.IsZero() || report.Node != node.IP || report.RequiresReboot != nil {
			t.Fatalf("invalid running image observation for %s", node.IP)
		}
		t.Logf("node=%s extensions=%d schematic=%s consistent=%t", node.IP, len(report.Extensions), report.SchematicID, report.Consistent)
	}
}

func TestVanillaSchematicPlainInstallerCompatibility(t *testing.T) {
	const vanilla = "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"
	for _, tc := range []struct {
		image, id  string
		extensions []string
		allowed    bool
	}{
		{"ghcr.io/siderolabs/installer:v1.14.0", vanilla, nil, true},
		{"ghcr.io/siderolabs/installer-amd64:v1.14.0", vanilla, nil, true},
		{"ghcr.io/siderolabs/installer:v1.14.0", strings.Repeat("a", 64), nil, false},
		{"ghcr.io/siderolabs/installer:v1.14.0", vanilla, []string{"qemu-guest-agent"}, false},
		{"factory.talos.dev/metal-installer/" + strings.Repeat("a", 64) + ":v1.14.0", vanilla, nil, false},
	} {
		if got := validateInstallerRuntime(tc.image, tc.id, tc.extensions) == nil; got != tc.allowed {
			t.Fatalf("image=%s allowed=%v want=%v", tc.image, got, tc.allowed)
		}
	}
}

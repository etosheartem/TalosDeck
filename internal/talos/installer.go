package talos

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/siderolabs/talos/pkg/machinery/client"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/resources/config"
	"github.com/siderolabs/talos/pkg/machinery/resources/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type InstalledExtension struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type NodeImageStatus struct {
	Node           string               `json:"node"`
	SchematicID    string               `json:"schematicId"`
	InstallerImage string               `json:"installerImage"`
	Extensions     []InstalledExtension `json:"extensions"`
	RequiresReboot *bool                `json:"requiresReboot"`
	CheckedAt      time.Time            `json:"checkedAt"`
	Consistent     bool                 `json:"consistent"`
}

// GetNodeImageStatus reads running extensions separately from the configured
// installer. A mismatch remains visible to the inspector and blocks upgrades.
// Running inventory does not prove whether a staged configuration needs reboot.
func (m *TalosManager) GetNodeImageStatus(ctx context.Context, node string) (NodeImageStatus, error) {
	return GetNodeImageStatusWithClient(ctx, m.GetClient(), node)
}

// GetNodeImageStatusWithClient also supports freshly provisioned clients before registry import.
func GetNodeImageStatusWithClient(ctx context.Context, c *client.Client, node string) (NodeImageStatus, error) {
	out := NodeImageStatus{Node: node, Extensions: []InstalledExtension{}}
	if c == nil {
		return out, fmt.Errorf("Talos client unavailable")
	}
	nodeCtx := client.WithNode(ctx, node)
	mc, err := safe.StateGet[*talosconfig.MachineConfig](nodeCtx, c.COSI, resource.NewMetadata("config", talosconfig.MachineConfigType, talosconfig.ActiveID, resource.VersionUndefined))
	if err != nil || mc == nil || mc.Provider() == nil || mc.Provider().Machine() == nil || mc.Provider().Machine().Install() == nil {
		return out, fmt.Errorf("configured installer unavailable")
	}
	out.InstallerImage = mc.Provider().Machine().Install().Image()
	schematic, err := safe.StateGet[*runtime.ImageFactorySchematic](nodeCtx, c.COSI, resource.NewMetadata(runtime.NamespaceName, runtime.ImageFactorySchematicType, runtime.ImageFactorySchematicID, resource.VersionUndefined))
	if err != nil && !schematicResourceUnavailable(err) {
		return out, fmt.Errorf("running schematic unavailable")
	}
	if err == nil && schematic != nil {
		out.SchematicID = schematic.TypedSpec().SchematicID
	}
	extensions, err := safe.StateListAll[*runtime.ExtensionStatus](nodeCtx, c.COSI)
	if err != nil {
		return out, fmt.Errorf("installed extensions unavailable")
	}
	names := []string{}
	for extension := range extensions.All() {
		if extension == nil {
			continue
		}
		metadata := extension.TypedSpec().Metadata
		if metadata.Name == "schematic" {
			if out.SchematicID != "" && out.SchematicID != metadata.Version {
				return out, fmt.Errorf("running schematic sources disagree")
			}
			out.SchematicID = metadata.Version
			continue
		}
		names = append(names, metadata.Name)
		out.Extensions = append(out.Extensions, InstalledExtension{Name: metadata.Name, Version: metadata.Version})
	}
	if out.SchematicID != "" && !schematicHash.MatchString(out.SchematicID) {
		return out, fmt.Errorf("invalid running schematic")
	}
	sort.Slice(out.Extensions, func(i, j int) bool { return out.Extensions[i].Name < out.Extensions[j].Name })
	out.Consistent = validateInstallerRuntime(out.InstallerImage, out.SchematicID, names) == nil
	out.CheckedAt = time.Now().UTC()
	return out, nil
}

// GetInstallerImage preserves the running schematic and refuses a configured
// image that would silently remove or replace installed extensions.
func (m *TalosManager) GetInstallerImage(ctx context.Context, node string) (string, error) {
	out, err := m.GetNodeImageStatus(ctx, node)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(out.Extensions))
	for _, extension := range out.Extensions {
		names = append(names, extension.Name)
	}
	if err := validateInstallerRuntime(out.InstallerImage, out.SchematicID, names); err != nil {
		return "", fmt.Errorf("%s: %w", node, err)
	}
	return out.InstallerImage, nil
}

// Talos 1.13 exposes the schematic as an ExtensionStatus. Its resource allowlist
// returns PermissionDenied (not NotFound) for the newer resource type. Match that
// exact server response; an actual authorization failure must still fail closed.
func schematicResourceUnavailable(err error) bool {
	if state.IsNotFoundError(err) {
		return true
	}
	st, ok := status.FromError(err)
	return ok && st.Code() == codes.PermissionDenied && st.Message() == fmt.Sprintf("resource type %q is not supported", runtime.ImageFactorySchematicType)
}

var schematicHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validateInstallerRuntime(image, schematicID string, extensions []string) error {
	const vanilla = "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"
	if schematicID == vanilla {
		if len(extensions) > 0 {
			return fmt.Errorf("vanilla schematic conflicts with installed extensions")
		}
		if regexp.MustCompile(`^ghcr\.io/siderolabs/installer(?:-amd64)?:v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(image) {
			return nil
		}
	}
	if schematicID == "" {
		if len(extensions) > 0 {
			return fmt.Errorf("installed extensions have no verifiable schematic; configure an Image Factory installer preserving those extensions before upgrading")
		}
		if strings.Contains(image, "factory.talos.dev/") {
			return fmt.Errorf("cannot verify the running Image Factory schematic; inspect the node before upgrading")
		}
		return nil
	}
	if !schematicHash.MatchString(schematicID) {
		return fmt.Errorf("running Image Factory schematic ID is invalid")
	}
	colon := strings.LastIndex(image, ":")
	if colon < 0 {
		return fmt.Errorf("configured installer must include a version tag")
	}
	path := image[:colon]
	slash := strings.LastIndex(path, "/")
	if slash < 0 || path[slash+1:] != schematicID {
		return fmt.Errorf("configured installer does not match the running schematic %s; update its image repository to preserve installed extensions", schematicID)
	}
	return nil
}

package talos

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/siderolabs/talos/pkg/machinery/client"
	talosconfig "github.com/siderolabs/talos/pkg/machinery/resources/config"
	"github.com/siderolabs/talos/pkg/machinery/resources/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetInstallerImage returns only the configured installer, never the machine's
// credentials. Keeping its repository/schematic preserves system extensions.
func (m *TalosManager) GetInstallerImage(ctx context.Context, node string) (string, error) {
	c := m.GetClient()
	if c == nil {
		return "", fmt.Errorf("Talos client unavailable")
	}
	mc, err := safe.StateGet[*talosconfig.MachineConfig](client.WithNode(ctx, node), c.COSI, resource.NewMetadata("config", talosconfig.MachineConfigType, talosconfig.ActiveID, resource.VersionUndefined))
	if err != nil {
		return "", err
	}
	if mc == nil || mc.Provider() == nil || mc.Provider().Machine() == nil || mc.Provider().Machine().Install() == nil {
		return "", fmt.Errorf("installer image unavailable on %s", node)
	}
	image := mc.Provider().Machine().Install().Image()
	nodeCtx := client.WithNode(ctx, node)
	schematicID := ""
	schematic, err := safe.StateGet[*runtime.ImageFactorySchematic](nodeCtx, c.COSI, resource.NewMetadata(runtime.NamespaceName, runtime.ImageFactorySchematicType, runtime.ImageFactorySchematicID, resource.VersionUndefined))
	if err != nil && !schematicResourceUnavailable(err) {
		return "", fmt.Errorf("cannot verify running image schematic on %s", node)
	}
	if err == nil && schematic != nil {
		schematicID = schematic.TypedSpec().SchematicID
	}
	extensions, err := safe.StateListAll[*runtime.ExtensionStatus](nodeCtx, c.COSI)
	if err != nil {
		return "", fmt.Errorf("cannot verify installed extensions on %s", node)
	}
	names := []string{}
	for extension := range extensions.All() {
		if extension == nil {
			continue
		}
		metadata := extension.TypedSpec().Metadata
		if metadata.Name == "schematic" {
			if schematicID != "" && schematicID != metadata.Version {
				return "", fmt.Errorf("running image schematic sources disagree on %s", node)
			}
			if schematicID == "" {
				schematicID = metadata.Version
			}
			continue
		}
		names = append(names, metadata.Name)
	}
	if err := validateInstallerRuntime(image, schematicID, names); err != nil {
		return "", fmt.Errorf("%s: %w", node, err)
	}
	return image, nil
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

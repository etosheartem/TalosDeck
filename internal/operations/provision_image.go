package operations

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"talosdeck/internal/imagefactory"
	"talosdeck/internal/jobs"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/talos"
)

type FactoryImages interface {
	Resolve(context.Context, string, string) (imagefactory.Profile, error)
	Checksum(context.Context, imagefactory.Profile) (string, error)
}

var schematicPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (s *ProvisionService) resolveProvisionImage(ctx context.Context, spec *ProvisionSpec) (*imagefactory.Profile, error) {
	if spec.SchematicID == "" {
		if spec.ISOStorage != "" || spec.Architecture != "" || spec.Platform != "" {
			return nil, errors.New("Factory image fields require a schematic")
		}
		return nil, nil
	}
	if spec.Kind != "cluster-create" && spec.Kind != "worker-create" {
		return nil, errors.New("Factory images are only supported for provisioning")
	}
	if !schematicPattern.MatchString(spec.SchematicID) || spec.Architecture != "amd64" || spec.Platform != "metal" || spec.ISOStorage == "" {
		return nil, errors.New("Factory provisioning requires a valid schematic, amd64, metal and ISO storage")
	}
	for _, m := range spec.Machines {
		if m.ISO != "" {
			return nil, errors.New("manual boot ISO cannot be combined with a Factory profile")
		}
	}
	if unavailable(s.Images) {
		return nil, errors.New("Image Factory is unavailable")
	}
	p, err := s.Images.Resolve(ctx, spec.SchematicID, "v"+strings.TrimPrefix(spec.TalosVersion, "v"))
	if err != nil {
		return nil, err
	}
	qga := false
	for _, ext := range p.Extensions {
		qga = qga || ext.Name == "siderolabs/qemu-guest-agent" || ext.Name == "qemu-guest-agent"
	}
	if !qga {
		return nil, errors.New("Proxmox Factory images require qemu-guest-agent")
	}
	if p.ID != spec.SchematicID || p.Architecture != spec.Architecture || p.Platform != spec.Platform || strings.TrimPrefix(p.Version, "v") != strings.TrimPrefix(spec.TalosVersion, "v") {
		return nil, errors.New("Factory profile does not match requested image")
	}
	if spec.InstallerImage != "" && spec.InstallerImage != p.InstallerImage {
		return nil, errors.New("installer image does not match the selected Factory boot profile")
	}
	spec.InstallerImage = p.InstallerImage
	return &p, nil
}
func (s *ProvisionService) imagePreflight(ctx context.Context, p proxmox.MachineProvider, spec ProvisionSpec) error {
	if spec.SchematicID == "" {
		return providerPreflight(ctx, p, spec.Machines)
	}
	fp, ok := p.(proxmox.FactoryImageProvider)
	if !ok {
		return errors.New("provider cannot prepare verified Factory images")
	}
	return fp.PreflightFactoryMachines(ctx, spec.Machines, spec.ISOStorage)
}
func (s *ProvisionService) prepareImage(ctx context.Context, e *jobs.Execution, plan *provisionState, p proxmox.MachineProvider) error {
	if plan.Spec.SchematicID == "" {
		return nil
	}
	spec := plan.Spec
	fresh, err := s.resolveProvisionImage(ctx, &spec)
	if err != nil {
		return err
	}
	if plan.ImageProfile == nil {
		return errors.New("Factory plan has no pinned image profile")
	}
	old := *plan.ImageProfile
	current := *fresh
	old.CheckedAt = current.CheckedAt
	if !reflect.DeepEqual(old, current) {
		return errors.New("Factory profile changed; create a new provisioning plan")
	}
	if err := s.imagePreflight(ctx, p, spec); err != nil {
		return err
	}
	if err := e.Checkpoint(ctx, "prepare-image", "Verifying Factory ISO and preparing provider storage"); err != nil {
		return err
	}
	digest, err := s.Images.Checksum(ctx, *fresh)
	if err != nil {
		return errors.New("cannot verify Factory ISO checksum")
	}
	plan.ImageFilename = "talosdeck-" + plan.ID + ".iso"
	plan.ImageChecksum = digest
	if err := s.save(ctx, plan, false); err != nil {
		return err
	}
	fp := p.(proxmox.FactoryImageProvider)
	task, err := fp.DownloadISO(ctx, spec.ISOStorage, plan.ImageFilename, fresh.ISOURL, digest)
	if err != nil {
		if errors.Is(err, proxmox.ErrImageDownloadUncertain) {
			return jobs.ErrUncertain
		}
		return err
	}
	plan.ImageTask = task
	if err := s.save(ctx, plan, false); err != nil {
		return jobs.ErrUncertain
	}
	if err := e.Log("prepare-image-task", "ISO "+spec.ISOStorage+":iso/"+plan.ImageFilename+"; provider task "+task); err != nil {
		return jobs.ErrUncertain
	}
	if err := p.WaitTask(ctx, task); err != nil {
		return jobs.ErrUncertain
	}
	if err := fp.VerifyISO(ctx, spec.ISOStorage, plan.ImageFilename); err != nil {
		return err
	}
	if err := e.Log("prepare-image-ready", "Verified ISO "+plan.ImageFilename+"; SHA256 "+digest); err != nil {
		return err
	}
	for i := range plan.Spec.Machines {
		plan.Spec.Machines[i].ISO = spec.ISOStorage + ":iso/" + plan.ImageFilename
	}
	return s.save(ctx, plan, false)
}
func validateUpgradeFactory(ctx context.Context, images FactoryImages, current, target string) error {
	canonical := strings.HasPrefix(current, "factory.talos.dev/")
	tag := strings.LastIndex(current, ":")
	slash := strings.LastIndex(current, "/")
	if tag <= slash {
		if canonical {
			return errors.New("invalid Factory installer")
		}
		return nil
	}
	repository := current[:tag]
	parts := strings.Split(repository, "/")
	id := parts[len(parts)-1]
	// A verified schematic can also be installed through a private registry.
	// Retain that repository, but still check the schematic against the target catalog.
	if !schematicPattern.MatchString(id) {
		if canonical {
			return errors.New("invalid current Factory schematic")
		}
		return nil
	}
	if unavailable(images) {
		return errors.New("Image Factory is unavailable; cannot verify target extensions")
	}
	profile, err := images.Resolve(ctx, id, "v"+strings.TrimPrefix(target, "v"))
	if err != nil {
		return err
	}
	if profile.ID != id || strings.TrimPrefix(profile.Version, "v") != strings.TrimPrefix(target, "v") {
		return errors.New("target Factory profile mismatch")
	}
	return nil
}

func validateInstalledProfile(profile imagefactory.Profile, status talos.NodeImageStatus) error {
	if !status.Consistent || status.SchematicID != profile.ID || status.InstallerImage != profile.InstallerImage {
		return errors.New("installed schematic or installer does not match the confirmed Factory profile")
	}
	expected := map[string]bool{}
	for _, ext := range profile.Extensions {
		expected[strings.TrimPrefix(ext.Name, "siderolabs/")] = true
	}
	installed := map[string]bool{}
	for _, ext := range status.Extensions {
		installed[strings.TrimPrefix(ext.Name, "siderolabs/")] = true
	}
	if !reflect.DeepEqual(expected, installed) {
		return errors.New("installed extensions do not match the confirmed Factory profile")
	}
	return nil
}

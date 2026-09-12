package operations

import (
	"context"
	"errors"
	"strings"
	"talosdeck/internal/imagefactory"
	"talosdeck/internal/jobs"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/talos"
	"testing"
)

type fakeFactoryImages struct {
	fail     bool
	changed  bool
	hashFail bool
}

func (f *fakeFactoryImages) Resolve(_ context.Context, id, version string) (imagefactory.Profile, error) {
	if !strings.HasPrefix(version, "v") {
		return imagefactory.Profile{}, errors.New("version must be normalized")
	}
	if f.fail {
		return imagefactory.Profile{}, errors.New("extension unavailable")
	}
	p := imagefactory.Profile{ID: id, Version: "v" + strings.TrimPrefix(version, "v"), Architecture: "amd64", Platform: "metal", InstallerImage: "factory.talos.dev/metal-installer/" + id + ":v" + strings.TrimPrefix(version, "v"), ISOURL: "https://factory.talos.dev/image/" + id + "/v" + strings.TrimPrefix(version, "v") + "/metal-amd64.iso", Extensions: []imagefactory.Extension{{Name: "siderolabs/qemu-guest-agent", Digest: "sha256:original"}}}
	if f.changed {
		p.Extensions[0].Digest = "sha256:changed"
	}
	return p, nil
}
func (f *fakeFactoryImages) Checksum(context.Context, imagefactory.Profile) (string, error) {
	if f.hashFail {
		return "", errors.New("hash failure")
	}
	return strings.Repeat("b", 64), nil
}

type imageProvider struct {
	failCreateProvider
	downloads              int
	downloadFail, taskFail bool
	bootISO                string
}

func (f *imageProvider) PreflightFactoryMachines(context.Context, []proxmox.MachineSpec, string) error {
	return nil
}
func (f *imageProvider) DownloadISO(context.Context, string, string, string, string) (string, error) {
	f.downloads++
	if f.downloadFail {
		return "", errors.New("download denied")
	}
	return "UPID:image", nil
}
func (f *imageProvider) VerifyISO(context.Context, string, string) error { return nil }
func (f *imageProvider) WaitTask(context.Context, string) error {
	if f.taskFail {
		return errors.New("checksum mismatch")
	}
	return nil
}
func (f *imageProvider) CreateMachine(_ context.Context, s proxmox.MachineSpec, _ proxmox.OwnedMachine) (string, error) {
	f.created++
	f.bootISO = s.ISO
	return "", errors.New("stop before real VM")
}
func TestFactoryProvisionRejectsMismatchedInputsBeforeProvider(t *testing.T) {
	for _, mode := range []string{"installer", "iso", "schematic", "catalog"} {
		t.Run(mode, func(t *testing.T) {
			s, spec, _ := provisionFixture(t)
			images := &fakeFactoryImages{}
			s.Images = images
			spec.SchematicID = strings.Repeat("a", 64)
			spec.Architecture = "amd64"
			spec.Platform = "metal"
			spec.ISOStorage = "local"
			spec.InstallerImage = ""
			switch mode {
			case "installer":
				spec.InstallerImage = "ghcr.io/siderolabs/installer:v1.14.0"
			case "iso":
				spec.Machines[0].ISO = "local:iso/old.iso"
			case "schematic":
				spec.SchematicID = "bad"
			case "catalog":
				images.fail = true
			}
			s.ProviderFactory = func(ProviderRecord) (proxmox.MachineProvider, error) {
				t.Fatal("provider reached for invalid profile")
				return nil, nil
			}
			if _, err := s.Plan(context.Background(), spec, "admin"); err == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}
func TestFactoryDownloadAndPinnedCatalogFailBeforeVM(t *testing.T) {
	for _, mode := range []string{"download", "task", "catalog-change", "checksum", "prepared"} {
		t.Run(mode, func(t *testing.T) {
			s, spec, _ := provisionFixture(t)
			images := &fakeFactoryImages{}
			s.Images = images
			provider := &imageProvider{}
			s.ProviderFactory = func(ProviderRecord) (proxmox.MachineProvider, error) { return provider, nil }
			spec.SchematicID = strings.Repeat("a", 64)
			spec.Architecture = "amd64"
			spec.Platform = "metal"
			spec.ISOStorage = "images"
			spec.InstallerImage = ""
			plan, err := s.Plan(context.Background(), spec, "admin")
			if err != nil {
				t.Fatal(err)
			}
			if plan.ImageProfile == nil || !strings.Contains(plan.Spec.InstallerImage, spec.SchematicID) {
				t.Fatal("profile not pinned")
			}
			switch mode {
			case "download":
				provider.downloadFail = true
			case "task":
				provider.taskFail = true
			case "catalog-change":
				images.changed = true
			case "checksum":
				images.hashFail = true
			}
			request, err := s.Request(context.Background(), plan.ID, spec.Name, "admin")
			if err != nil {
				t.Fatal(err)
			}
			job := runJob(t, &Service{Provision: s}, request)
			expected := "failed"
			if mode == "prepared" || mode == "task" {
				expected = "interrupted"
			}
			if job.Status != expected {
				t.Fatalf("unexpected %s", job.Status)
			}
			if mode == "prepared" {
				if provider.created != 1 || provider.bootISO != "images:iso/talosdeck-"+plan.ID+".iso" {
					t.Fatal("VM did not use newly verified ISO")
				}
			} else if provider.created != 0 {
				t.Fatal("VM mutation before verified image")
			}
			state, err := s.load(context.Background(), plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "task" && state.ImageTask != "UPID:image" {
				t.Fatal("download task not durably recorded")
			}
		})
	}
}
func TestUpgradeFactoryChecksTargetCatalog(t *testing.T) {
	f := newFixture()
	svc := service(f)
	svc.Images = &fakeFactoryImages{fail: true}
	if _, err := svc.Preflight(context.Background(), jobs.Request{Kind: "talos-upgrade", Version: "1.14.0", AllowDowntime: true}); err == nil {
		t.Fatal("unsupported target extensions accepted")
	}
}

func TestUpgradeMirroredFactoryRetainsSchematicValidation(t *testing.T) {
	image := "registry.example:5000/talos/metal-installer/" + strings.Repeat("a", 64) + ":v1.13.0"
	if err := validateUpgradeFactory(context.Background(), &fakeFactoryImages{fail: true}, image, "1.14.0"); err == nil {
		t.Fatal("mirrored schematic skipped target validation")
	}
	if err := validateUpgradeFactory(context.Background(), &fakeFactoryImages{}, image, "1.14.0"); err != nil {
		t.Fatal(err)
	}
	target, err := installerFor(image, "1.14.0")
	if err != nil || target != "registry.example:5000/talos/metal-installer/"+strings.Repeat("a", 64)+":v1.14.0" {
		t.Fatalf("mirror changed: %s %v", target, err)
	}
}

func TestInstalledProfileRequiresMatchingRuntimeAndExtensions(t *testing.T) {
	profile, _ := (&fakeFactoryImages{}).Resolve(context.Background(), strings.Repeat("a", 64), "v1.14.0")
	valid := talos.NodeImageStatus{SchematicID: profile.ID, InstallerImage: profile.InstallerImage, Consistent: true, Extensions: []talos.InstalledExtension{{Name: "qemu-guest-agent", Version: "10"}}}
	if err := validateInstalledProfile(profile, valid); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"schematic", "installer", "missing", "extra", "inconsistent"} {
		t.Run(mode, func(t *testing.T) {
			status := valid
			switch mode {
			case "schematic":
				status.SchematicID = strings.Repeat("b", 64)
			case "installer":
				status.InstallerImage = "ghcr.io/siderolabs/installer:v1.14.0"
			case "missing":
				status.Extensions = nil
			case "extra":
				status.Extensions = append(append([]talos.InstalledExtension{}, valid.Extensions...), talos.InstalledExtension{Name: "unexpected"})
			case "inconsistent":
				status.Consistent = false
			}
			if err := validateInstalledProfile(profile, status); err == nil {
				t.Fatal("runtime mismatch accepted")
			}
		})
	}
}

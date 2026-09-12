package talos

import (
	"github.com/siderolabs/talos/pkg/machinery/resources/block"
	"testing"
)

func TestRestorePartitionRequiresRecognizedBackingVolume(t *testing.T) {
	parent := block.VolumeStatusSpec{Type: block.VolumeTypePartition, Location: "/dev/vda6", MountSpec: block.MountSpec{TargetPath: "/var"}}
	for _, tc := range []struct {
		name string
		etcd *block.VolumeStatusSpec
		want string
	}{
		{"legacy", nil, "EPHEMERAL"},
		{"dedicated", &block.VolumeStatusSpec{Type: block.VolumeTypePartition, Location: "/dev/vda7", MountSpec: block.MountSpec{TargetPath: "/var/lib/etcd"}}, "ETCD"},
		{"directory inside ephemeral", &block.VolumeStatusSpec{Type: block.VolumeTypeDirectory, ParentID: "EPHEMERAL", MountSpec: block.MountSpec{TargetPath: "/var/lib/etcd"}}, "EPHEMERAL"},
		{"directory inside STATE", &block.VolumeStatusSpec{Type: block.VolumeTypeDirectory, ParentID: "STATE", MountSpec: block.MountSpec{TargetPath: "/var/lib/etcd"}}, ""},
		{"external", &block.VolumeStatusSpec{Type: block.VolumeTypeExternal, MountSpec: block.MountSpec{TargetPath: "/var/lib/etcd"}}, ""},
		{"wrong mount", &block.VolumeStatusSpec{Type: block.VolumeTypePartition, Location: "/dev/vda7", MountSpec: block.MountSpec{TargetPath: "/system/state"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specs := map[string]block.VolumeStatusSpec{"EPHEMERAL": parent}
			if tc.etcd != nil {
				specs["ETCD"] = *tc.etcd
			}
			got, err := restorePartitionFromVolumes(specs)
			if tc.want == "" {
				if err == nil {
					t.Fatal("unsafe backing volume accepted")
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
	if _, err := restorePartitionFromVolumes(map[string]block.VolumeStatusSpec{"STATE": parent}); err == nil {
		t.Fatal("STATE selected")
	}
}

func TestRestorePartitionResolvesActualTalos114DirectoryChain(t *testing.T) {
	fixture := func() map[string]block.VolumeStatusSpec {
		return map[string]block.VolumeStatusSpec{
			"EPHEMERAL": {Type: block.VolumeTypePartition, Location: "/dev/sda6", MountSpec: block.MountSpec{TargetPath: "/var"}},
			"/var/lib":  {Type: block.VolumeTypeDirectory, MountSpec: block.MountSpec{ParentID: "EPHEMERAL", TargetPath: "lib"}},
			"ETCD":      {Type: block.VolumeTypeDirectory, MountSpec: block.MountSpec{ParentID: "/var/lib", TargetPath: "etcd"}},
		}
	}
	if got, err := restorePartitionFromVolumes(fixture()); err != nil || got != "EPHEMERAL" {
		t.Fatalf("actual Talos1.14 chain refused: %s %v", got, err)
	}
	for _, failure := range []string{"cycle", "missing parent", "symlink", "traversal", "STATE backing", "wrong resolved path", "bind mount"} {
		t.Run(failure, func(t *testing.T) {
			specs := fixture()
			etcd, parent := specs["ETCD"], specs["/var/lib"]
			switch failure {
			case "cycle":
				parent.MountSpec.ParentID = "ETCD"
			case "missing parent":
				parent.MountSpec.ParentID = "missing"
			case "symlink":
				parent.Type = block.VolumeTypeSymlink
			case "traversal":
				etcd.MountSpec.TargetPath = "../etcd"
			case "STATE backing":
				specs["STATE"] = specs["EPHEMERAL"]
				parent.MountSpec.ParentID = "STATE"
			case "wrong resolved path":
				etcd.MountSpec.TargetPath = "other"
			case "bind mount":
				target := "/var/lib/etcd"
				etcd.MountSpec.BindTarget = &target
			}
			specs["ETCD"], specs["/var/lib"] = etcd, parent
			if _, err := restorePartitionFromVolumes(specs); err == nil {
				t.Fatal("unsafe mount chain accepted")
			}
		})
	}
}

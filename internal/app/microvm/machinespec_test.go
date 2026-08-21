package microvm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/networking"
	vmruntime "github.com/CYPT71/platform-factory/internal/runtime"
)

func machineDigest(value byte) string { return "sha256:" + strings.Repeat(string(value), 64) }

func TestBuildMachineSpecBindsVerifiedRuntimeRequirements(t *testing.T) {
	source := t.TempDir()
	spec, err := BuildMachineSpec(MachineSpecOptions{
		ID: "demo", KernelDigest: machineDigest('1'), InitramfsDigest: machineDigest('2'),
		ManifestDigest: machineDigest('3'), RootFSDigest: machineDigest('4'),
		CommandLine: []string{"console=hvc0", "rdinit=/sbin/init"}, MemoryMiB: 256, VCPUs: 2,
		RequiredPorts: []string{"8080/tcp"}, RequiredVolumes: []string{"/data"},
		Forwards: []networking.Forward{{HostIP: "127.0.0.1", HostPort: 18080, GuestPort: 8080, Protocol: "tcp"}},
		Volumes:  []VolumeMapping{{Source: source, Target: "/data", ReadOnly: true}},
		DNS:      []string{"1.1.1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := vmruntime.ValidateBootBundle(spec.Bundle); err != nil {
		t.Fatalf("invalid boot bundle: %v", err)
	}
	if spec.Bundle.Metadata["platform-factory.dev/oci-manifest"] != machineDigest('3') ||
		spec.Bundle.Metadata["platform-factory.dev/rootfs-format"] != "initramfs" {
		t.Fatalf("metadata=%v", spec.Bundle.Metadata)
	}
	if len(spec.Ports) != 1 || spec.Ports[0].Guest != 8080 || len(spec.Volumes) != 1 || spec.Volumes[0].Target != "/data" || !spec.Volumes[0].ReadOnly {
		t.Fatalf("ports=%+v volumes=%+v", spec.Ports, spec.Volumes)
	}
}

func TestBuildMachineSpecRefusesUnresolvedOrInventedRequirements(t *testing.T) {
	source := t.TempDir()
	base := MachineSpecOptions{ID: "demo", KernelDigest: machineDigest('1'), InitramfsDigest: machineDigest('2'), ManifestDigest: machineDigest('3'), RootFSDigest: machineDigest('4'), MemoryMiB: 128, VCPUs: 1}
	tests := []struct {
		name string
		edit func(*MachineSpecOptions)
	}{
		{"missing port", func(o *MachineSpecOptions) { o.RequiredPorts = []string{"80/tcp"} }},
		{"missing volume", func(o *MachineSpecOptions) { o.RequiredVolumes = []string{"/data"} }},
		{"extra volume", func(o *MachineSpecOptions) { o.Volumes = []VolumeMapping{{Source: source, Target: "/data"}} }},
		{"invalid dns", func(o *MachineSpecOptions) { o.DNS = []string{"not-an-address"} }},
		{"invalid manifest", func(o *MachineSpecOptions) { o.ManifestDigest = "latest" }},
		{"non hexadecimal manifest", func(o *MachineSpecOptions) { o.ManifestDigest = "sha256:" + strings.Repeat("z", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := base
			test.edit(&options)
			if _, err := BuildMachineSpec(options); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}

	target := filepath.Join(t.TempDir(), "target")
	if err := os.Symlink(source, target); err != nil {
		t.Fatal(err)
	}
	base.RequiredVolumes = []string{"/data"}
	base.Volumes = []VolumeMapping{{Source: target, Target: "/data"}}
	if _, err := BuildMachineSpec(base); err == nil {
		t.Fatal("expected symlink source rejection")
	}
}

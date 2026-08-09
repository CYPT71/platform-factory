package kubevirt

import (
	"strings"
	"testing"

	microvm "github.com/CYPT71/secure-oci-base/sdk/microvm"
)

func TestValidate(t *testing.T) {
	spec := microvm.Spec{
		Name: "demo", Namespace: "default", MemoryMiB: 256, VCPUs: 2, Port: 8080,
		Image: "registry.example/boot@sha256:" + strings.Repeat("a", 64), Arch: "amd64", Listen: "127.0.0.1",
	}
	if err := Validate(spec); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	base := microvm.Spec{Name: "demo", Namespace: "default", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080}
	for _, spec := range []microvm.Spec{
		base,
		{Name: "demo", Namespace: "Bad", Image: "registry/image@sha256:" + strings.Repeat("a", 64), Arch: "amd64", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080},
		{Name: "demo", Namespace: "default", Image: "registry/image:latest", Arch: "amd64", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080},
		{Name: "demo", Namespace: "default", Image: "registry/image@sha256:" + strings.Repeat("g", 64), Arch: "amd64", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080},
		{Name: "demo", Namespace: "default", Image: "registry/image@sha256:" + strings.Repeat("a", 64), Arch: "riscv64", Listen: "127.0.0.1", MemoryMiB: 128, VCPUs: 1, Port: 8080},
	} {
		if err := Validate(spec); err == nil {
			t.Fatalf("accepted %#v", spec)
		}
	}
}

func TestVirtualMachine(t *testing.T) {
	spec := microvm.Spec{
		Name: "demo", Namespace: "production", MemoryMiB: 256, VCPUs: 2, Port: 8080,
		Image: "registry.example/boot@sha256:" + strings.Repeat("b", 64), Arch: "amd64", Listen: "127.0.0.1",
	}
	output, err := VirtualMachine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"kind": "VirtualMachine"`, `"runStrategy": "Halted"`, spec.Image,
		`"app.kubernetes.io/managed-by": "platform-factory"`,
		`"kernelPath": "/boot/kernel"`, `"initrdPath": "/boot/initramfs.cpio.gz"`,
		`"kernelArgs": "console=ttyS0 rdinit=/sbin/init ip=dhcp panic=-1"`,
	} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("manifest missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(string(output), `"containerDisk"`) {
		t.Fatalf("external kernel boot manifest unexpectedly attaches a containerDisk:\n%s", output)
	}
}

func TestARM64AndNetworkPorts(t *testing.T) {
	spec := microvm.Spec{
		Name: "demo", Namespace: "production", MemoryMiB: 256, VCPUs: 2, Port: 8080,
		Image: "registry.example/boot@sha256:" + strings.Repeat("c", 64),
		Arch:  "arm64", Listen: "127.0.0.1",
		Forwards: []microvm.Forward{{HostPort: 8443, GuestPort: 443, Protocol: "tcp"}},
	}
	output, err := VirtualMachine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"architecture": "arm64"`, `"console=ttyAMA0`, `"port": 443`, `"protocol": "TCP"`} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("manifest missing %q:\n%s", want, output)
		}
	}
}

//go:build darwin && cgo

package hvf

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	api "github.com/CYPT71/platform-factory/internal/microvm"
	vmruntime "github.com/CYPT71/platform-factory/internal/runtime"
)

func TestDarwinMachineAgentUsesInjectedAuthenticatedConnector(t *testing.T) {
	host, guest := net.Pipe()
	defer guest.Close()
	key := bytes.Repeat([]byte{3}, 32)
	machine := &darwinMachine{
		id: "agent-test",
		agentConnector: func(_ context.Context, id string) (io.ReadWriteCloser, []byte, error) {
			if id != "agent-test" {
				t.Fatalf("connector id = %q", id)
			}
			return host, key, nil
		},
	}
	agent, err := machine.Agent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := agent.(io.Closer); !ok {
		t.Fatal("agent lifecycle is not closable")
	} else if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	unconfigured := &darwinMachine{id: "unconfigured"}
	if _, err := unconfigured.Agent(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured agent error = %v", err)
	}
}

func TestDarwinMachineAgentClaimsNativeHVC1ExactlyOnce(t *testing.T) {
	descriptors, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	host := os.NewFile(uintptr(descriptors[0]), "hvc1-host")
	guest := os.NewFile(uintptr(descriptors[1]), "hvc1-guest")
	defer guest.Close()
	key := bytes.Repeat([]byte{7}, 32)
	machine := &darwinMachine{
		id:        "native-agent",
		agentFile: host,
		agentKey: func(_ context.Context, id string) ([]byte, error) {
			if id != "native-agent" {
				t.Fatalf("key resolver id=%q", id)
			}
			return key, nil
		},
	}
	agent, err := machine.Agent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := agent.(io.Closer); !ok {
		t.Fatal("native agent is not closable")
	} else if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := machine.Agent(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("second native claim err=%v", err)
	}
}

func TestDarwinMachineNativeAgentRequiresProvisionedKey(t *testing.T) {
	descriptors, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	host := os.NewFile(uintptr(descriptors[0]), "hvc1-host")
	guest := os.NewFile(uintptr(descriptors[1]), "hvc1-guest")
	defer host.Close()
	defer guest.Close()
	machine := &darwinMachine{id: "missing-key", agentFile: host}
	if _, err := machine.Agent(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "key is not configured") {
		t.Fatalf("missing key err=%v", err)
	}
	if machine.agentClaimed {
		t.Fatal("missing key consumed the native channel")
	}
}

func dummyDigestResolver(t *testing.T, content string) (ContentResolver, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "content")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	return func(_ context.Context, requested string) (string, error) {
		if requested != digest {
			return "", errors.New("unexpected digest requested")
		}
		return path, nil
	}, digest
}

func TestDarwinVMMCreateFailsClosedForAmbiguousRootFSFormats(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	resolveCalls := 0
	vmm, err := NewDarwinVMM(func(context.Context, string) (string, error) {
		resolveCalls++
		return "", errors.New("resolver must not be reached")
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := vmruntime.NewBootBundle(digest, "", digest, []string{"console=hvc0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := api.MachineSpec{
		ID:        "compat-test",
		Bundle:    bundle,
		Resources: api.Resources{VCPUs: 1, MemoryMiB: 256},
		Ports:     []api.Port{{Host: 8080, Guest: 80}},
	}
	if _, err := vmm.Create(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "rootfs format") ||
		!strings.Contains(err.Error(), "port forwarding") {
		t.Fatalf("create err=%v", err)
	}
	if resolveCalls != 0 {
		t.Fatalf("content resolver called %d times before unsupported rootfs format was rejected", resolveCalls)
	}
}

func TestDarwinCapabilitiesAndUnsupportedMachineSpecFieldsAreExplicit(t *testing.T) {
	capabilities, err := ProbeNative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{
		"dns", "volumes", "guest-environment",
	} {
		if capabilities.Features[feature] {
			t.Fatalf("unsupported feature %q advertised: %+v", feature, capabilities)
		}
	}
	// network/port-forwarding are advertised (RunLinuxHVF's NAT-attached
	// virtio-net device) but flagged unverified-on-real-hardware in
	// Details - see docs/legacy-vm-disk-boot.md.
	if !capabilities.Features["network"] || !capabilities.Features["port-forwarding"] ||
		capabilities.Details["network-caveat"] == "" {
		t.Fatalf("network/port-forwarding not advertised with its caveat: %+v", capabilities)
	}
	if !capabilities.Features["create-vm"] || !capabilities.Features["rootfs"] ||
		capabilities.Details["rootfs-format"] != darwinRootFSFormatInitramfs {
		t.Fatalf("implemented rootfs transport not advertised: %+v", capabilities)
	}
	if capabilities.Available &&
		(!capabilities.Features["kernel-boot"] || !capabilities.Features["initrd"] ||
			!capabilities.Features["vcpu-memory"] || !capabilities.Features["serial-console"] ||
			!capabilities.Features["entropy"]) {
		t.Fatalf("available framework omitted implemented capabilities: %+v", capabilities)
	}

	tests := map[string]struct {
		spec api.MachineSpec
		want string
	}{
		"ports":   {spec: api.MachineSpec{Ports: []api.Port{{Host: 8080, Guest: 80}}}, want: "port forwarding"},
		"volumes": {spec: api.MachineSpec{Volumes: []api.Volume{{Source: "/source", Target: "/target"}}}, want: "volume attachment"},
		"dns":     {spec: api.MachineSpec{DNS: []string{"127.0.0.1"}}, want: "DNS configuration"},
		"env":     {spec: api.MachineSpec{Env: map[string]string{"KEY": "value"}}, want: "environment injection"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateDarwinMachineSpecSupport(tc.spec); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsupported %s err=%v", name, err)
			}
		})
	}
	if err := validateDarwinMachineSpecSupport(api.MachineSpec{}); err != nil {
		if !strings.Contains(err.Error(), "rootfs format") {
			t.Fatalf("unexpected empty support-only spec error: %v", err)
		}
	}
	supported := api.MachineSpec{Bundle: api.BootBundle{Metadata: map[string]string{
		darwinRootFSFormatKey: darwinRootFSFormatInitramfs,
	}}}
	if err := validateDarwinMachineSpecSupport(supported); err != nil {
		t.Fatalf("initramfs rootfs rejected: %v", err)
	}
	supported.Bundle.Initrd = "sha256:" + strings.Repeat("b", 64)
	if err := validateDarwinMachineSpecSupport(supported); err == nil ||
		!strings.Contains(err.Error(), "separate initrd") {
		t.Fatalf("separate initrd err=%v", err)
	}
}

func TestDarwinVMMRejectsInvalidMachineID(t *testing.T) {
	resolve, _ := dummyDigestResolver(t, "unused")
	vmm, err := NewDarwinVMM(resolve, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := api.MachineSpec{ID: "Not Valid!", Bundle: api.BootBundle{Kernel: "sha256:" + strings.Repeat("a", 64)}}
	if _, err := vmm.Create(context.Background(), spec); err == nil {
		t.Fatal("accepted an invalid machine id")
	}
}

func TestNewDarwinVMMRejectsMissingResolver(t *testing.T) {
	if _, err := NewDarwinVMM(nil, t.TempDir()); err == nil {
		t.Fatal("accepted a nil ContentResolver")
	}
}

func TestDarwinVMMRejectsDuplicateIDsAndInvalidResourcesBeforeFrameworkCalls(t *testing.T) {
	resolve, digest := dummyDigestResolver(t, "unused")
	vmm, err := NewDarwinVMM(resolve, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	vmm.machines["duplicate"] = &darwinMachine{id: "duplicate", freed: true}
	bundle, err := vmruntime.NewBootBundle(digest, "", digest, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := api.MachineSpec{
		ID:        "duplicate",
		Bundle:    bundle,
		Resources: api.Resources{VCPUs: 1, MemoryMiB: 256},
	}
	if _, err := vmm.Create(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create err=%v", err)
	}
	delete(vmm.machines, "duplicate")
	spec.ID = "invalid-resources"
	spec.Resources = api.Resources{}
	if _, err := vmm.Create(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "memory limits") {
		t.Fatalf("resource validation err=%v", err)
	}
}

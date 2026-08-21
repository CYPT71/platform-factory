// The native-KVM run/serve implementation lives here, not in
// microvm_native.go, because internal/hypervisor/kvm's TAP and
// RunLinuxWithOptions surface (OpenTAP, AssignTAPAddress,
// RunLinuxWithOptions, LinuxRunOptions, NetworkDeviceOptions) only
// exists for linux/amd64 - the same restriction kvm_run_linux_amd64.go
// itself already carries. microvm_native.go's nativeKVMEligible already
// runtime-gates this backend to linux/amd64 hosts, but Go type-checks
// every file in a package regardless of what's reachable at runtime, so
// the file boundary has to match the build tag boundary too.
// microvm_native_stub.go provides the non-(linux&&amd64)-non-darwin
// counterpart to runNativeKVM so runNativeKVMSubcommand, which calls it
// unconditionally, stays portable; microvm_native_darwin.go is darwin's
// own real implementation (HVF). findRepoRoot/readEntrypoint/
// readVerifiedBlob/nativeLog are shared by both in microvm_native_shared.go.
//go:build linux && amd64

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/CYPT71/platform-factory/internal/hypervisor/kvm"
	"github.com/CYPT71/platform-factory/internal/microvm/forward"
	"github.com/CYPT71/platform-factory/internal/networking"
	"github.com/CYPT71/platform-factory/internal/rootfs"
)

// runNativeKVM builds the guest initramfs from layout, resolves a kernel,
// boots it under internal/hypervisor/kvm with a TAP-backed virtio-net
// device on a fixed point-to-point address, and relays each forward's host
// port to the guest's fixed address for as long as the boot runs (until
// ctx is cancelled by a signal or the guest process exits).
func runNativeKVM(ctx context.Context, layoutDir string, memoryMiB int, forwards []networking.Forward, stdout, stderr io.Writer) (err error) {
	nativeLog(stderr, "phase=native-kvm-start layout=%s", layoutDir)

	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "platform-factory-microvm-native.*")
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	defer os.RemoveAll(work)

	nativeLog(stderr, "phase=build-init")
	initBinary := filepath.Join(work, "init")
	buildInit := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", initBinary, "./cmd/microvm-init")
	buildInit.Dir = repoRoot
	buildInit.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := buildInit.CombinedOutput(); err != nil {
		return fmt.Errorf("build microvm-init: %w: %s", err, strings.TrimSpace(string(out)))
	}

	nativeLog(stderr, "phase=rootfs converting verified OCI layout")
	convertedRoot := filepath.Join(work, "rootfs")
	convertResult, err := rootfs.Convert(rootfs.Options{Layout: layoutDir, Output: convertedRoot, Platform: "linux/amd64"})
	if err != nil {
		return fmt.Errorf("convert rootfs: %w", err)
	}
	if err := installNativeRuntimeContract(convertedRoot, initBinary, convertResult.Runtime, forwards); err != nil {
		return err
	}

	nativeLog(stderr, "phase=initramfs")
	initramfsPath := filepath.Join(work, "initramfs.cpio.gz")
	initramfsFile, err := os.Create(initramfsPath)
	if err != nil {
		return fmt.Errorf("create initramfs: %w", err)
	}
	if err := rootfs.WriteInitramfs(convertedRoot, initramfsFile); err != nil {
		_ = initramfsFile.Close()
		return fmt.Errorf("write initramfs: %w", err)
	}
	if err := initramfsFile.Close(); err != nil {
		return fmt.Errorf("close initramfs: %w", err)
	}
	initramfs, err := os.ReadFile(initramfsPath)
	if err != nil {
		return err
	}

	kernelPath := filepath.Join(repoRoot, ".cache", "microvm", "amd64", "kernel")
	nativeLog(stderr, "phase=kernel ensuring kernel path=%s", kernelPath)
	buildKernel := exec.CommandContext(ctx, filepath.Join(repoRoot, "scripts", "microvm", "build-kernel.sh"), "amd64", kernelPath)
	buildKernel.Dir = repoRoot
	if out, err := buildKernel.CombinedOutput(); err != nil {
		return fmt.Errorf("build kernel: %w: %s", err, strings.TrimSpace(string(out)))
	}
	kernel, err := os.ReadFile(kernelPath)
	if err != nil {
		return fmt.Errorf("read kernel: %w", err)
	}

	nativeLog(stderr, "phase=network opening TAP")
	tap, tapName, err := kvm.OpenTAP("")
	if err != nil {
		return fmt.Errorf("open TAP: %w", err)
	}
	tapOwned := true
	defer func() {
		if tapOwned {
			_ = tap.Close()
		}
	}()
	if err := kvm.AssignTAPAddress(tapName, nativeHostCIDR); err != nil {
		return fmt.Errorf("assign TAP address: %w", err)
	}
	mac := make(net.HardwareAddr, 6)
	if _, err := rand.Read(mac); err != nil {
		return fmt.Errorf("generate MAC: %w", err)
	}
	mac[0] = (mac[0] &^ 0x01) | 0x02 // locally administered, unicast

	commandLine := fmt.Sprintf(
		"console=ttyS0,115200 earlycon=uart,io,0x3f8,115200 ignore_loglevel panic=0 rdinit=/sbin/init "+
			"ip=%s::%s:%s::eth0:off",
		nativeGuestIP, nativeGatewayIP, nativeNetmask,
	)

	nativeLog(stderr, "phase=forward starting %d relay(s)", len(forwards))
	relayCtx, cancelRelays := context.WithCancel(ctx)
	defer cancelRelays()
	var relayGroup sync.WaitGroup
	for _, f := range forwards {
		hostIP := f.HostIP
		if hostIP == "" {
			hostIP = "127.0.0.1"
		}
		hostAddr := net.JoinHostPort(hostIP, strconv.Itoa(f.HostPort))
		guestAddr := net.JoinHostPort(nativeGuestIP, strconv.Itoa(f.GuestPort))
		relayGroup.Add(1)
		go func(protocol, hostAddr, guestAddr string) {
			defer relayGroup.Done()
			relay := forward.Relay
			if protocol == "udp" {
				relay = forward.RelayUDP
			}
			if err := relay(relayCtx, hostAddr, guestAddr); err != nil {
				nativeLog(stderr, "phase=forward error protocol=%s host=%s guest=%s err=%v", protocol, hostAddr, guestAddr, err)
			}
		}(f.Protocol, hostAddr, guestAddr)
	}
	defer relayGroup.Wait()

	nativeLog(stderr, "phase=boot backend=native-kvm memory-mib=%d", memoryMiB)
	tapOwned = false // RunLinuxWithOptions now owns closing it, see NetworkDeviceOptions's doc comment
	result, runErr := kvm.RunLinuxWithOptions(ctx, uint64(memoryMiB)<<20, kernel, initramfs, commandLine, 1<<20,
		kvm.LinuxRunOptions{
			SerialWriter:   stdout,
			NetworkDevices: []kvm.NetworkDeviceOptions{{TAP: tap, MAC: mac}},
		})
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
		return fmt.Errorf("run guest: %w", runErr)
	}
	nativeLog(stderr, "phase=boot-complete shutdown=%t halted=%t exits=%d", result.Shutdown, result.Halted, result.Exits)
	return nil
}

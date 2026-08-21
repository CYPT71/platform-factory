// The native-HVF run/serve implementation lives here, not in
// microvm_native.go, for the same reason microvm_native_linux_amd64.go
// is split out: it needs internal/hypervisor/hvf symbols that only build
// on darwin (cgo + Virtualization.framework). The build tag must require
// cgo explicitly, not just darwin - internal/hypervisor/hvf's own files
// all carry "darwin && cgo", so a CGO_ENABLED=0 darwin build (a real,
// supported configuration - see scripts/ci/build-mcp-image-layout.sh's
// host-side `go build` step) would otherwise resolve that import to zero
// files and fail with "build constraints exclude all Go files".
// microvm_native_stub.go provides the non-(linux&&amd64)-non-(darwin&&cgo)
// counterpart to runNativeKVM, covering exactly that CGO_ENABLED=0-on-
// darwin case in addition to every other unsupported platform.
//
// Real-hardware validation requires the macOS virtualization entitlement;
// see docs/legacy-vm-disk-boot.md.
//go:build darwin && cgo

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/CYPT71/platform-factory/internal/hypervisor/hvf"
	"github.com/CYPT71/platform-factory/internal/microvm/forward"
	"github.com/CYPT71/platform-factory/internal/networking"
	"github.com/CYPT71/platform-factory/internal/rootfs"
)

// guestIPReportMarker must match cmd/microvm-init's own copy exactly
// (net_report.go) - the two can't share a Go const across a package-main
// boundary, so this is a second, deliberate copy, not an accident.
const guestIPReportMarker = "PLATFORM-FACTORY-GUEST-IP="

// runNativeKVM builds the guest initramfs from layout (same as the Linux
// backend) and boots it under Apple's Virtualization.framework (see
// internal/hypervisor/hvf) with a NAT-attached virtio-net device. Unlike
// the KVM backend's fixed point-to-point address, the guest's address is
// only known once cmd/microvm-init reports it (over the serial console,
// after negotiating it via the kernel's own DHCP client - see
// scripts/microvm/kernel-common.config's CONFIG_IP_PNP_DHCP) - forwards
// are relayed to that address once seen, not before.
func runNativeKVM(ctx context.Context, layoutDir string, memoryMiB int, forwards []networking.Forward, stdout, stderr io.Writer) (err error) {
	nativeLog(stderr, "phase=native-hvf-start layout=%s", layoutDir)

	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "platform-factory-microvm-native-hvf.*")
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	defer os.RemoveAll(work)

	guestArch := runtime.GOARCH // HVF accelerates a guest matching the host architecture, same rule as KVM.

	nativeLog(stderr, "phase=build-init arch=%s", guestArch)
	initBinary := filepath.Join(work, "init")
	buildInit := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", initBinary, "./cmd/microvm-init")
	buildInit.Dir = repoRoot
	buildInit.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+guestArch)
	if out, err := buildInit.CombinedOutput(); err != nil {
		return fmt.Errorf("build microvm-init: %w: %s", err, strings.TrimSpace(string(out)))
	}

	nativeLog(stderr, "phase=rootfs converting verified OCI layout")
	convertedRoot := filepath.Join(work, "rootfs")
	convertResult, err := rootfs.Convert(rootfs.Options{Layout: layoutDir, Output: convertedRoot, Platform: "linux/" + guestArch})
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

	kernelPath := filepath.Join(repoRoot, ".cache", "microvm", guestArch, "kernel")
	nativeLog(stderr, "phase=kernel ensuring kernel path=%s", kernelPath)
	buildKernel := exec.CommandContext(ctx, filepath.Join(repoRoot, "scripts", "microvm", "build-kernel.sh"), guestArch, kernelPath)
	buildKernel.Dir = repoRoot
	if out, err := buildKernel.CombinedOutput(); err != nil {
		return fmt.Errorf("build kernel: %w: %s", err, strings.TrimSpace(string(out)))
	}

	mac, err := randomLocallyAdministeredMAC()
	if err != nil {
		return fmt.Errorf("generate MAC: %w", err)
	}

	// `ip=dhcp` (not KVM's static ip=<addr>::...): VZNATNetworkDeviceAttachment
	// gives the guest no fixed address of its own, so it must negotiate
	// one - the kernel's own built-in DHCP client does that, no userspace
	// client needed (CONFIG_IP_PNP_DHCP). console=hvc0 matches Virtualization.
	// framework's virtio-console serial port, not KVM's 16550 UART (ttyS0).
	commandLine := "console=hvc0 earlycon=hvc0 ignore_loglevel panic=0 rdinit=/sbin/init ip=dhcp"

	relayCtx, cancelRelays := context.WithCancel(ctx)
	defer cancelRelays()
	var relayGroup sync.WaitGroup
	defer relayGroup.Wait()
	liveWriter := &ipWatchingWriter{
		passthrough: stdout,
		onGuestIP: func(guestIP string) {
			nativeLog(stderr, "phase=network guest reported address=%s starting %d relay(s)", guestIP, len(forwards))
			for _, f := range forwards {
				hostIP := f.HostIP
				if hostIP == "" {
					hostIP = "127.0.0.1"
				}
				hostAddr := net.JoinHostPort(hostIP, strconv.Itoa(f.HostPort))
				guestAddr := net.JoinHostPort(guestIP, strconv.Itoa(f.GuestPort))
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
		},
	}

	nativeLog(stderr, "phase=boot backend=native-hvf memory-mib=%d mac=%s", memoryMiB, mac)
	result, runErr := hvf.RunLinuxHVF(ctx, kernelPath, initramfsPath, commandLine, "",
		uint64(memoryMiB)<<20, 1, mac, liveWriter)
	if runErr != nil && ctx.Err() == nil {
		return fmt.Errorf("run guest: %w", runErr)
	}
	nativeLog(stderr, "phase=boot-complete stopped=%t serial-matched=%t", result.Stopped, result.SerialMatched)
	return nil
}

// ipWatchingWriter passes every byte through to the real console
// (passthrough - normally stdout, so the operator sees boot output
// exactly as before) while separately watching the accumulated stream
// for cmd/microvm-init's DHCP-address report line, invoking onGuestIP
// exactly once when it appears.
type ipWatchingWriter struct {
	passthrough io.Writer
	onGuestIP   func(guestIP string)

	mu        sync.Mutex
	buf       bytes.Buffer
	triggered bool
}

func (w *ipWatchingWriter) Write(p []byte) (int, error) {
	if _, err := w.passthrough.Write(p); err != nil {
		return 0, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.triggered {
		return len(p), nil
	}
	w.buf.Write(p)
	if idx := bytes.Index(w.buf.Bytes(), []byte(guestIPReportMarker)); idx >= 0 {
		rest := w.buf.Bytes()[idx+len(guestIPReportMarker):]
		if newline := bytes.IndexByte(rest, '\n'); newline >= 0 {
			guestIP := strings.TrimSpace(string(rest[:newline]))
			w.triggered = true
			if net.ParseIP(guestIP) != nil {
				w.onGuestIP(guestIP)
			}
		}
	}
	return len(p), nil
}

// randomLocallyAdministeredMAC mirrors the KVM backend's own MAC
// generation (microvm_native_linux_amd64.go): a random address with the
// locally-administered bit set and the multicast bit cleared, formatted
// for vz_create_machine's mac_address parameter.
func randomLocallyAdministeredMAC() (string, error) {
	mac := make(net.HardwareAddr, 6)
	if _, err := rand.Read(mac); err != nil {
		return "", err
	}
	mac[0] = (mac[0] &^ 0x01) | 0x02
	return mac.String(), nil
}

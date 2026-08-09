package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/CYPT71/secure-oci-base/internal/hypervisor"
	"github.com/CYPT71/secure-oci-base/internal/microvm"
	"github.com/CYPT71/secure-oci-base/internal/networking"
)

// The native-KVM `microvm run/start` backend replaces run-microvm.sh's
// QEMU boot with internal/hypervisor/kvm.RunLinuxWithOptions directly, once
// virtio-blk/virtio-net made that viable (see the roadmap entry this
// change closes). QEMU's SLIRP `hostfwd=` has no equivalent in this VMM's
// TAP-based networking, so port publishing - the command's primary use
// case, always active via at least one synthesized default forward, see
// microvm.go's spec-building code - is reproduced with a fixed
// point-to-point TAP link plus a host-side TCP relay
// (internal/microvm/forward) rather than real NAT. See nativeKVMEligible
// for the exact, narrower conditions under which this backend is used at
// all; every other case still runs run-microvm.sh/QEMU unchanged.
const (
	nativeGuestIP   = "169.254.100.2"
	nativeHostCIDR  = "169.254.100.1/30"
	nativeNetmask   = "255.255.255.252"
	nativeGatewayIP = "169.254.100.1"
)

// nativeKVMEligible reports whether spec's run/start can use a native
// backend (KVM on linux/amd64, HVF on darwin - see nativeBackendImplemented)
// instead of run-microvm.sh/QEMU, and if not, why (logged, not silently
// decided). Despite the name (kept for now to avoid a wide rename), this
// has been capability-aware and cross-platform since the HVF backend
// gained networking (5 août 2026, UNVERIFIED on real hardware - see
// docs/legacy-vm-disk-boot.md) - it no longer hardcodes linux/amd64.
func nativeKVMEligible(ctx context.Context, spec microvm.Spec) (bool, string) {
	if !nativeBackendImplemented() {
		return false, fmt.Sprintf("no native backend is wired up for %s/%s yet, falling back to QEMU", runtime.GOOS, runtime.GOARCH)
	}
	if spec.VCPUs != 1 {
		return false, "native backend currently supports exactly one vCPU"
	}
	capabilities, err := hypervisor.ProbeNative(ctx)
	if err != nil {
		return false, fmt.Sprintf("native probe failed: %v", err)
	}
	if !capabilities.Available {
		return false, fmt.Sprintf("native backend unavailable: %s", capabilities.Details["unavailable"])
	}
	if len(spec.Forwards) > 0 && !capabilities.Features["port-forwarding"] {
		return false, fmt.Sprintf("native backend (%s) does not support port forwarding", capabilities.Details["backend"])
	}
	for _, f := range spec.Forwards {
		if f.Protocol != "tcp" {
			return false, fmt.Sprintf("native backend only relays TCP forwards, got %q for port %d", f.Protocol, f.HostPort)
		}
	}
	return true, ""
}

// nativeBackendImplemented reports whether this GOOS/GOARCH has an actual
// runNativeKVM implementation to dispatch to at all - independent of
// whether the hardware/entitlement/etc. underneath it actually works
// (that's ProbeNative's job). linux/amd64 has microvm_native_linux_amd64.go
// (KVM); darwin has microvm_native_darwin.go (HVF, any arch Vz supports).
// Every other GOOS/GOARCH only has microvm_native_stub.go, which always
// errors, so nativeKVMEligible must never return true for them.
func nativeBackendImplemented() bool {
	return (runtime.GOOS == "linux" && runtime.GOARCH == "amd64") || runtime.GOOS == "darwin"
}

// nativeRunArgs builds the `microvm __run-native` argv this process
// re-execs itself with, mirroring the LaunchSupervisor/`__serve` re-exec
// pattern in internal/ociruntime: run/start's own dispatch stays a plain
// "pick a command and argv" decision, and the actual native boot always
// happens in a fresh child process, exactly as it already did when that
// command was run-microvm.sh - `start`'s systemd-run wrapping in
// runNative therefore needs no changes at all to wrap this instead.
func nativeRunArgs(spec microvm.Spec) []string {
	args := []string{
		"microvm", "__run-native",
		"--layout", spec.Layout,
		"--memory-mib", strconv.Itoa(spec.MemoryMiB),
	}
	for _, f := range spec.Forwards {
		hostIP := f.HostIP
		if hostIP == "" {
			hostIP = spec.Listen
		}
		args = append(args, "--publish", fmt.Sprintf("%s|%s|%d|%d", f.Protocol, hostIP, f.HostPort, f.GuestPort))
	}
	return args
}

// runNativeKVMSubcommand is the `__run-native` handler: parse the argv
// nativeRunArgs built and run the boot to completion. Not reachable from
// the public `run|start|...` usage string - it is an internal re-exec
// target, the same relationship `platform-factory-runtime __serve` has to
// `platform-factory-runtime create/start`.
func runNativeKVMSubcommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("microvm __run-native", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layout := flags.String("layout", "", "verified local OCI layout")
	memoryMiB := flags.Int("memory-mib", 128, "guest memory in MiB")
	var publishes repeatedFlag
	flags.Var(&publishes, "publish", "proto|hostip|hostport|guestport; repeatable")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *layout == "" {
		fmt.Fprintln(stderr, "platform-factory microvm __run-native: --layout is required")
		return 2
	}
	var forwards []networking.Forward
	for _, encoded := range publishes {
		parts := strings.Split(encoded, "|")
		if len(parts) != 4 {
			fmt.Fprintf(stderr, "platform-factory microvm __run-native: malformed --publish %q\n", encoded)
			return 2
		}
		hostPort, err1 := strconv.Atoi(parts[2])
		guestPort, err2 := strconv.Atoi(parts[3])
		if err1 != nil || err2 != nil {
			fmt.Fprintf(stderr, "platform-factory microvm __run-native: malformed --publish %q\n", encoded)
			return 2
		}
		forwards = append(forwards, networking.Forward{
			Protocol: parts[0], HostIP: parts[1], HostPort: hostPort, GuestPort: guestPort,
		})
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer cancel()
	if err := runNativeKVM(ctx, *layout, *memoryMiB, forwards, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "platform-factory microvm run (native KVM): %v\n", err)
		return 1
	}
	return 0
}

// nativeLog, runNativeKVM and its helpers (findRepoRoot, readEntrypoint,
// readVerifiedBlob) live in microvm_native_linux_amd64.go, not here - see
// that file's comment for why.

# Native HVF networking — status and how to verify it

Tracks generalizing `pf microvm run/start`'s native-backend dispatch
beyond Linux/KVM to also cover macOS's native Virtualization.framework
backend (`internal/hypervisor/hvf`), including giving that backend its
first network device. Written 2026-08-06 in a sandboxed environment with
**no test kernel image and no code-signing entitlement available** - see
"What is genuinely unverified" below before relying on any of this.

## What changed

- **`pf microvm run/start` dispatch is no longer hardcoded to
  linux/amd64.** `nativeKVMEligible` (`cmd/platform-factory/microvm_native.go`)
  now checks `nativeBackendImplemented()` (linux/amd64 *or* darwin,
  any arch) and is capability-aware: it checks
  `hypervisor.ProbeNative().Features["port-forwarding"]` before treating
  a spec with any `--publish` as eligible, rather than assuming every
  native backend can relay ports the way KVM's TAP+relay setup already
  does. This is a real behavior change with real test coverage: all 4
  tests that used to fail on this darwin host because eligibility was
  hardcoded to Linux now pass because eligibility is honest about what's
  actually available, not just because a message string was tweaked.
- **The HVF backend has its first network device.**
  `internal/hypervisor/hvf/vz_bridge_darwin.{h,m}` now optionally attach
  a `VZVirtioNetworkDeviceConfiguration` with a `VZNATNetworkDeviceAttachment`,
  given a MAC address. `RunLinuxHVF` gained two new parameters:
  `macAddress` (empty = no network device, matching old behavior) and
  `liveWriter` (an `io.Writer` that receives serial output incrementally
  as it's polled, not just once at the end - needed for the next point).
- **`cmd/platform-factory/microvm_native_darwin.go`** is the darwin
  counterpart to `microvm_native_linux_amd64.go`: builds the guest
  init/rootfs/initramfs the same way, boots via `hvf.RunLinuxHVF` with a
  freshly generated locally-administered MAC, and relays each
  `--publish` forward once (not before) the guest reports its own
  address.
- **The guest reports its own address.** Unlike KVM's fixed
  `169.254.100.2` (static `ip=` kernel parameter), a NAT-attached guest
  has no address until it negotiates one via DHCP, so there's nothing
  to relay to until it does. `cmd/microvm-init` (`net_report_linux.go`)
  now checks `/proc/cmdline` for the literal field `ip=dhcp` (present
  only on the HVF boot path; the KVM path's static `ip=<addr>::...`
  never matches) and, only then, polls `eth0` for an IPv4 address and
  writes `PLATFORM-FACTORY-GUEST-IP=<addr>` to the serial console once
  found. The host's `ipWatchingWriter` scans for that exact line and
  starts the relay(s) the moment it sees one. **The kernel does the DHCP
  negotiation itself** (`CONFIG_IP_PNP_DHCP`, added to
  `scripts/microvm/kernel-common.config`) - no userspace DHCP client
  was written; the KVM path's static `CONFIG_IP_PNP` behavior is
  unaffected by this addition.

## What is genuinely unverified

Be precise about this distinction when deciding how much to trust it:

- **Compiles and passes real, non-skipped tests**: the Objective-C
  bridge change (`vz_create_machine` correctly rejects a malformed MAC
  address, in a real `go test` run, in this sandbox, no entitlement
  needed - `TestRunLinuxHVFRejectsMalformedMACAddress`), the capability
  report shape, the eligibility dispatch logic (all 4 originally-failing
  tests now pass for real, exercising the actual `nativeKVMEligible`/
  `runNative` dispatch code with a mocked executor), the DHCP-cmdline
  gating logic (`cmdlineRequestsDHCP`), and the guest-IP-marker parsing
  (`ipWatchingWriter`, including a split-across-writes case).
- **Never executed at all, in any form, by anyone, as of this commit**:
  whether `VZNATNetworkDeviceAttachment` + the kernel's `ip=dhcp` client
  actually negotiate a working address; whether the host can actually
  reach that address (NAT semantics on the current macOS version);
  whether the serial console reliably delivers `cmd/microvm-init`'s
  report line before the host's poll loop needs it; whether the overall
  boot even completes at all under HVF with a network device attached
  (previously, no HVF boot in this repo's test suite has ever had one).
  None of this can be exercised without a real kernel image and a
  code-signed, entitled binary - both absent from the environment that
  wrote this feature.

## How to verify on a real Mac

1. **Build a real kernel + initramfs test fixture.**
   `scripts/microvm/build-kernel.sh arm64 /tmp/kernel` (or `amd64` on
   Intel) builds a kernel with the new `CONFIG_IP_PNP_DHCP`. Any Linux
   initramfs works for `PLATFORM_FACTORY_TEST_INITRD`; the repo's own
   `examples/sdk/microvm` or `assemble-initramfs.sh` output does.
2. **Sign the test binary with the virtualization entitlement.**
   `internal/hypervisor/hvf`'s own real-hardware tests
   (`TestRunLinuxWithRealHVF`, `TestDarwinVMMWithRealHVF`) already
   assume this - see `scripts/microvm/hvf.entitlements` and codesign the
   compiled test binary (`go test -c`) with it, or build+sign
   `platform-factory` itself before invoking `pf microvm run` directly.
3. **Run the existing gated tests first** (isolate the new networking
   code from the CLI dispatch layer):
   ```sh
   PLATFORM_FACTORY_TEST_KERNEL_IMAGE=/tmp/kernel \
   PLATFORM_FACTORY_TEST_INITRD=/tmp/initramfs.cpio.gz \
     go test ./internal/hypervisor/hvf/... -run RealHVF -v
   ```
   These don't attach a network device yet (they call `RunLinuxHVF`
   with an empty MAC) - passing confirms the *existing*, previously-real
   boot path still works after this change's edits to the same function.
4. **Then exercise networking specifically**: build an OCI layout with
   `platform-factory build`, then
   `platform-factory microvm run --backend=native --layout <layout> --publish 8080:8080`
   on the signed binary. Watch stderr for `phase=network guest reported
   address=... starting N relay(s)`; if that line never appears, the
   guest either never got a DHCP lease or the marker never reached the
   serial console - check the kernel actually has `CONFIG_IP_PNP_DHCP`
   compiled in (`zcat /proc/config.gz | grep IP_PNP` inside a running
   guest, or `strings` the kernel image for the string) before assuming
   the Go/ObjC code is at fault.
5. **Report back what broke.** The most likely failure points, in
   rough order of suspicion: (a) the kernel not actually getting a DHCP
   lease at all (Virtualization.framework's DHCP server behavior is the
   least-controlled part of this), (b) the host being unable to reach
   the guest's NAT-assigned address even once it has one, (c) something
   in the ObjC bridge's device configuration being subtly wrong in a way
   that only surfaces at `validateWithError:`/`startWithCompletionHandler:`
   time, which this session could never reach.

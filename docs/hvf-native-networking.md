# Native HVF networking — status and how to verify it

Tracks generalizing `pf microvm run/start`'s native-backend dispatch
beyond Linux/KVM to also cover macOS's native Virtualization.framework
backend (`internal/hypervisor/hvf`), including giving that backend its
first network device. The complete path is now tested on Apple-silicon
hardware with an entitled binary, an arm64 Linux guest and real TCP and UDP
traffic.

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

## Runtime evidence and remaining limits

- `TestNativeHVFRealTCPAndUDP` builds the Linux/arm64 example OCI image,
  boots it through `runNativeKVM`'s Darwin/HVF implementation, observes the
  kernel DHCP lease and guest IP marker, then requires successful HTTP and
  UDP echo responses through host loopback forwards.
- `tests/microvm/test-hvf-network-local.sh` compiles and entitlement-signs
  the exact CLI-package test binary. `ci-microvm.yml` runs the same gate.
- The test exposed a real missing prerequisite: `/proc` was not mounted, so
  PID 1 could not read `/proc/cmdline` and never reported its DHCP address.
  `microvm-init` now mounts proc with `nosuid,nodev,noexec` before reporting.
- Remaining limitation: `VZNATNetworkDeviceAttachment` gives the guest
  outbound connectivity. There is not yet an outbound network-policy engine,
  so this path must not be described as host-network isolated by default.

## How to verify on a real Mac

1. Build or restore `.cache/microvm/arm64/kernel` with
   `scripts/microvm/build-kernel.sh arm64 .cache/microvm/arm64/kernel`.
2. Run the native boot, Rosetta and network gates:
   ```sh
   tests/microvm/test-hvf-local.sh
   tests/microvm/test-hvf-network-local.sh
   ```
3. To exercise another workload, build an OCI layout with
   `platform-factory build`, then
   `platform-factory microvm run --backend=native --layout <layout>
   --publish 8080:8080 --publish 5353:53/udp` on an entitlement-signed
   binary.

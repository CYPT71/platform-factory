// vz_bridge_darwin.h is the C surface a Go/cgo caller uses to drive
// Apple's Virtualization.framework (Objective-C only, no C API of its own).
// Every function is synchronous: the implementation blocks internally on
// the virtual machine's own serial dispatch queue until the framework's
// asynchronous completion handler fires, so Go never has to bridge an
// Objective-C block or run its own dispatch/run-loop machinery.
#ifndef PLATFORM_FACTORY_VMM_VZ_BRIDGE_DARWIN_H
#define PLATFORM_FACTORY_VMM_VZ_BRIDGE_DARWIN_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

// vz_is_supported reports whether VZVirtualMachine.isSupported is true for
// this host (hardware capability and OS version; it does not by itself
// prove the calling process holds the com.apple.security.virtualization
// entitlement - a missing entitlement instead makes every configuration
// fail to validate).
int vz_is_supported(void);

// vz_machine_t is an opaque handle owned by the Objective-C side; release
// it with vz_machine_free exactly once, after the machine is stopped.
typedef struct vz_machine vz_machine_t;

// vz_create_machine builds a VZVirtualMachineConfiguration for a Linux
// guest (VZLinuxBootLoader + VZGenericPlatformConfiguration, one
// virtio-console serial port mirrored to serial_log_path, an optional second
// bidirectional virtio-console port backed by a native socketpair, one virtio
// entropy device, no storage device yet), validates it with
// -[VZVirtualMachineConfiguration validateWithError:], and on success
// retains a VZVirtualMachine ready to start. initrd_path may be NULL.
// When guest_agent_fd_out is non-NULL, ownership of the host socketpair fd is
// transferred to the caller on success; the guest endpoint is retained by the
// machine and conventionally appears as /dev/hvc1.
//
// When mac_address is non-NULL (a "xx:xx:xx:xx:xx:xx" string), a single
// VZVirtioNetworkDeviceConfiguration is attached via
// VZNATNetworkDeviceAttachment - NAT to the host, the guest negotiates its
// own address (the caller is responsible for the guest running a DHCP
// client; this project's own kernel config enables the kernel's built-in
// one via `ip=dhcp`, no userspace client needed). mac_address NULL means
// no network device at all, matching the previous no-network behavior.
// UNVERIFIED ON REAL HARDWARE as of the commit that added this parameter -
// see docs/legacy-vm-disk-boot.md's HVF networking section: written and
// compile-checked, never runtime-tested (no signed entitlement, no test
// kernel in the environment that wrote it).
// Returns NULL and fills error_out (a caller-owned buffer of error_cap
// bytes, always NUL-terminated on failure) otherwise.
vz_machine_t *vz_create_machine(
    const char *kernel_path, const char *initrd_path, const char *command_line,
    const char *serial_log_path, unsigned long long memory_bytes, unsigned int vcpu_count,
    const char *mac_address, int *guest_agent_fd_out, char *error_out, size_t error_cap);

// vz_machine_start blocks until -[VZVirtualMachine startWithCompletionHandler:]
// completes. Returns 0 on success, non-zero with error_out filled otherwise.
int vz_machine_start(vz_machine_t *machine, char *error_out, size_t error_cap);

// vz_machine_stop blocks until -[VZVirtualMachine stopWithCompletionHandler:]
// completes. This is the framework's hard stop (no guest-cooperative ACPI
// shutdown negotiation yet - see the roadmap's separate guest-agent/channel
// item). Returns 0 on success, non-zero with error_out filled otherwise.
// Calling it when the machine is already stopped is a documented no-op
// success.
int vz_machine_stop(vz_machine_t *machine, char *error_out, size_t error_cap);

// vz_machine_state returns the raw VZVirtualMachineState value.
int vz_machine_state(vz_machine_t *machine);

// vz_machine_free releases the retained VZVirtualMachine and its
// configuration. The machine must not be running.
void vz_machine_free(vz_machine_t *machine);

#ifdef __cplusplus
}
#endif
#endif

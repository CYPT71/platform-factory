//go:build linux

package kvm

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	api "github.com/CYPT71/platform-factory/internal/microvm"
)

const (
	kvmGetAPIVersion    = uintptr(0xAE00)
	kvmCheckExtension   = uintptr(0xAE03)
	kvmExpectedAPI      = 12
	kvmCapUserMemory    = uintptr(3)
	kvmCapImmediateExit = uintptr(136)
)

var requiredKVMExtensions = []struct {
	name string
	id   uintptr
}{
	{name: "user-memory", id: kvmCapUserMemory},
	{name: "immediate-exit", id: kvmCapImmediateExit},
}

// ProbeNative reports the actual KVM API exposed by /dev/kvm without invoking
// another VMM or a helper executable.
func ProbeNative(ctx context.Context) (api.Capabilities, error) {
	result := api.Capabilities{
		Architecture: runtime.GOARCH, Features: map[string]bool{},
		Details: map[string]string{"backend": "linux-kvm-native"},
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	file, err := os.OpenFile("/dev/kvm", os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		result.Details["unavailable"] = fmt.Sprintf("open /dev/kvm: %v", err)
		return result, nil
	}
	defer file.Close()
	version, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), kvmGetAPIVersion, uintptr(unsafe.Pointer(nil)))
	if errno != 0 {
		result.Details["unavailable"] = fmt.Sprintf("KVM_GET_API_VERSION: %v", errno)
		return result, nil
	}
	result.Details["api_version"] = fmt.Sprintf("%d", version)
	if version != kvmExpectedAPI {
		result.Details["unavailable"] = fmt.Sprintf("KVM API %d is unsupported; expected %d", version, kvmExpectedAPI)
		return result, nil
	}
	if err := negotiateRequiredKVMExtensions(kvmExtensionChecker(file), result.Features, result.Details); err != nil {
		result.Details["unavailable"] = err.Error()
		return result, nil
	}
	result.Available = true
	result.Features["create-vm"] = true
	// runNativeKVM (cmd/platform-factory/microvm_native_linux_amd64.go) relays
	// every forward over its point-to-point TAP link via a host TCP relay
	// instead of a QEMU SLIRP hostfwd equivalent, but it does relay them -
	// this capability was simply never advertised here, so
	// nativeKVMEligible rejected any spec with a forward and fell back to
	// QEMU even though native KVM handles it.
	result.Features["port-forwarding"] = true
	return result, nil
}

func kvmExtensionChecker(file *os.File) func(uintptr) (uintptr, error) {
	return func(extension uintptr) (uintptr, error) {
		value, _, errno := syscall.Syscall(
			syscall.SYS_IOCTL,
			file.Fd(),
			kvmCheckExtension,
			extension,
		)
		if errno != 0 {
			return 0, errno
		}
		return value, nil
	}
}

func negotiateRequiredKVMExtensions(check func(uintptr) (uintptr, error), features map[string]bool, details map[string]string) error {
	for _, extension := range requiredKVMExtensions {
		value, err := check(extension.id)
		if err != nil {
			return fmt.Errorf("KVM_CHECK_EXTENSION(%s): %w", extension.name, err)
		}
		supported := value != 0
		features["kvm."+extension.name] = supported
		details["kvm."+extension.name] = fmt.Sprintf("%d", value)
		if !supported {
			return fmt.Errorf("required KVM extension %s is unavailable", extension.name)
		}
	}
	return nil
}

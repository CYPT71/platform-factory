//go:build windows

// Package vmm's windows backend drives the Windows Hypervisor Platform
// (WHP, WinHvPlatform.dll) directly via syscall - no cgo, no external
// module, matching this repository's minimal-dependency convention. WHP
// must first be enabled as a Windows optional feature
// (Microsoft-Hyper-V-Hypervisor / "Windows Hypervisor Platform") before
// WHvGetCapability reports it present.
//
// Verification status: every constant and struct size below was confirmed
// against the real winhvplatformdefs.h header (via mingw-w64, which ships
// Microsoft's public WHP headers) with a compile-time _Static_assert, the
// same technique used for the native Linux KVM primitive. What is NOT
// verified is runtime behavior: this development environment has no
// Windows host, so ProbeNative below has never actually executed against
// a real WinHvPlatform.dll. Treat it as reviewed, not tested, until it
// runs on real Windows.
package whpx

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	api "github.com/CYPT71/platform-factory/internal/microvm"
)

var (
	winHvPlatformDLL     = syscall.NewLazyDLL("WinHvPlatform.dll")
	procWHvGetCapability = winHvPlatformDLL.NewProc("WHvGetCapability")
)

// WHvCapabilityCodeHypervisorPresent, confirmed == 0 against
// winhvplatformdefs.h.
const whvCapabilityCodeHypervisorPresent = 0

// whvCapabilitySize is sizeof(WHV_CAPABILITY): a union whose largest
// member is WHV_PROCESSOR_FEATURES_BANKS at 24 bytes
// (WHV_PROCESSOR_FEATURES_BANKS_COUNT=2, sized as 8*(2+1) per the
// header's own C_ASSERT). HypervisorPresent (a WINBOOL, 4 bytes) is the
// union's first member, so it lives at offset 0 regardless of the
// union's total size.
const whvCapabilitySize = 24

// ProbeNative reports Windows Hypervisor Platform availability through
// WHvGetCapability. It does not create a partition or virtual processor.
func ProbeNative(ctx context.Context) (api.Capabilities, error) {
	result := api.Capabilities{
		Architecture: runtime.GOARCH, Features: map[string]bool{},
		Details: map[string]string{"backend": "windows-native-whp"},
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := winHvPlatformDLL.Load(); err != nil {
		result.Details["unavailable"] = fmt.Sprintf("WinHvPlatform.dll: %v", err)
		return result, nil
	}
	if err := procWHvGetCapability.Find(); err != nil {
		result.Details["unavailable"] = fmt.Sprintf("WHvGetCapability: %v", err)
		return result, nil
	}

	var capability [whvCapabilitySize]byte
	var written uint32
	hresult, _, _ := procWHvGetCapability.Call(
		uintptr(whvCapabilityCodeHypervisorPresent),
		uintptr(unsafe.Pointer(&capability[0])),
		uintptr(len(capability)),
		uintptr(unsafe.Pointer(&written)),
	)
	// HRESULT is a signed 32-bit value; only the low 32 bits of the
	// returned uintptr are meaningful on Windows/amd64's calling
	// convention for a 32-bit return type.
	if code := int32(uint32(hresult)); code < 0 {
		result.Details["unavailable"] = fmt.Sprintf("WHvGetCapability failed: HRESULT 0x%08x", uint32(hresult))
		return result, nil
	}
	if written < 4 {
		result.Details["unavailable"] = fmt.Sprintf("WHvGetCapability wrote %d bytes, want at least 4", written)
		return result, nil
	}
	present := *(*int32)(unsafe.Pointer(&capability[0]))
	if present == 0 {
		result.Details["unavailable"] = "WHvCapabilityCodeHypervisorPresent reported false; enable the Windows Hypervisor Platform optional feature"
		return result, nil
	}
	result.Available = true
	result.Features["hypervisor-present"] = true
	return result, nil
}

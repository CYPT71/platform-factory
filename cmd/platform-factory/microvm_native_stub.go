// See microvm_native_linux_amd64.go's own comment for why this split
// exists: the real runNativeKVM needs internal/hypervisor/kvm symbols
// that only exist for linux/amd64. microvm_native_darwin.go is darwin's
// own real implementation (HVF). This stub exists purely so
// runNativeKVMSubcommand (microvm_native.go, portable) still type-checks
// on every other platform; nativeKVMEligible already refuses this
// backend before runNativeKVM would ever be reached here in practice.
//go:build !(linux && amd64) && !darwin

package main

import (
	"context"
	"fmt"
	"io"
	"runtime"

	"github.com/CYPT71/secure-oci-base/internal/networking"
)

func runNativeKVM(_ context.Context, _ string, _ int, _ []networking.Forward, _, _ io.Writer) error {
	return fmt.Errorf("native KVM backend requires linux/amd64, host is %s/%s", runtime.GOOS, runtime.GOARCH)
}

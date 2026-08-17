// Unsupported platforms reject the native backend before reaching this
// stub - including a CGO_ENABLED=0 darwin build, which has neither this
// stub (excluded by !darwin) nor microvm_native_darwin.go's real HVF
// backend (which requires "darwin && cgo") without the "&& cgo" here too.
//go:build !(linux && amd64) && !(darwin && cgo)

package main

import (
	"context"
	"fmt"
	"io"
	"runtime"

	"github.com/CYPT71/platform-factory/internal/networking"
)

func runNativeKVM(_ context.Context, _ string, _ int, _ []networking.Forward, _, _ io.Writer) error {
	return fmt.Errorf("native KVM backend requires linux/amd64, host is %s/%s", runtime.GOOS, runtime.GOARCH)
}

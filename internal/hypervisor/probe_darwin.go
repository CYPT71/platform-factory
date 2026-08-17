//go:build darwin && cgo

package hypervisor

import (
	"context"

	"github.com/CYPT71/platform-factory/internal/hypervisor/hvf"
	api "github.com/CYPT71/platform-factory/internal/microvm"
)

// ProbeNative reports native HVF availability on macOS.
func ProbeNative(ctx context.Context) (api.Capabilities, error) { return hvf.ProbeNative(ctx) }

//go:build windows

package hypervisor

import (
	"context"

	"github.com/CYPT71/platform-factory/internal/hypervisor/whpx"
	api "github.com/CYPT71/platform-factory/internal/microvm"
)

// ProbeNative reports native WHPX availability on Windows.
func ProbeNative(ctx context.Context) (api.Capabilities, error) { return whpx.ProbeNative(ctx) }

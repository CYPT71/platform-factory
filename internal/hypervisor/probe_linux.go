//go:build linux

package hypervisor

import (
	"context"

	"github.com/CYPT71/platform-factory/internal/hypervisor/kvm"
	api "github.com/CYPT71/platform-factory/internal/microvm"
)

// ProbeNative reports native KVM availability on Linux.
func ProbeNative(ctx context.Context) (api.Capabilities, error) { return kvm.ProbeNative(ctx) }

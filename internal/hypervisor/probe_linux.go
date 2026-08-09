//go:build linux

package hypervisor

import (
	"context"

	"github.com/CYPT71/secure-oci-base/internal/hypervisor/kvm"
	api "github.com/CYPT71/secure-oci-base/internal/microvm"
)

// ProbeNative reports native KVM availability on Linux.
func ProbeNative(ctx context.Context) (api.Capabilities, error) { return kvm.ProbeNative(ctx) }

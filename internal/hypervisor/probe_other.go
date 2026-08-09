//go:build !linux && !windows && (!darwin || !cgo)

package hypervisor

import (
	"context"

	api "github.com/CYPT71/secure-oci-base/internal/microvm"
)

// ProbeNative reports that no native backend is compiled for this platform.
func ProbeNative(ctx context.Context) (api.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return api.Capabilities{}, err
	}
	return api.Capabilities{Available: false, Details: map[string]string{"unavailable": "no native hypervisor backend compiled"}}, nil
}

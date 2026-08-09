//go:build !linux && !windows && !(darwin && cgo)

package kvm

import (
	"context"
	"runtime"

	api "github.com/CYPT71/secure-oci-base/internal/microvm"
)

func ProbeNative(ctx context.Context) (api.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return api.Capabilities{}, err
	}
	return api.Capabilities{
		Architecture: runtime.GOARCH,
		Details: map[string]string{
			"backend":     runtime.GOOS + "-native",
			"unavailable": "the native host adapter is not implemented yet",
		},
	}, nil
}

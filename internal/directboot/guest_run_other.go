//go:build !(linux && amd64)

package directboot

import (
	"context"
	"errors"
)

// RunWithGuestAgent is kept in the cross-platform API but fails explicitly on
// backends that do not yet expose a bidirectional guest device.
func RunWithGuestAgent(ctx context.Context, _ Config, _ GuestAgentOptions) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return Result{}, errors.New("direct boot: authenticated guest agent channel is currently only available on Linux/amd64")
}

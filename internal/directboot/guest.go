package directboot

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/CYPT71/platform-factory/internal/guesttransport"
	api "github.com/CYPT71/platform-factory/internal/microvm"
)

// GuestAgentOptions enables the authenticated COM2 guest channel for direct
// Linux boots. SessionKey must be the same per-boot key already provisioned in
// the pinned initramfs. OnReady must retain the agent and return promptly;
// RunWithGuestAgent drives the VM after OnReady returns.
type GuestAgentOptions struct {
	SessionKey []byte
	OnReady    func(api.GuestAgent) error
}

func prepareGuestAgent(ctx context.Context, options GuestAgentOptions) (api.GuestAgent, io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(options.SessionKey) < 32 {
		return nil, nil, errors.New("direct boot: guest session key must contain at least 32 bytes")
	}
	if options.OnReady == nil {
		return nil, nil, errors.New("direct boot: guest agent OnReady callback is required")
	}
	host, guest := net.Pipe()
	agent, err := guesttransport.NewAgent(host, options.SessionKey)
	if err != nil {
		_ = host.Close()
		_ = guest.Close()
		return nil, nil, err
	}
	if err := options.OnReady(agent); err != nil {
		_ = agent.Close()
		_ = guest.Close()
		return nil, nil, err
	}
	return agent, guest, nil
}

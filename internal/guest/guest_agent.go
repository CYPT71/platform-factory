package guest

import (
	"context"
	"errors"
	"io"

	"github.com/CYPT71/platform-factory/internal/guesttransport"
	api "github.com/CYPT71/platform-factory/internal/microvm"
)

// GuestAgentConnector opens one authenticated byte stream for a machine and
// returns its per-boot session key. Backends inject a vsock/virtio connector
// once the corresponding device exists; tests may use an in-memory socket.
type GuestAgentConnector func(context.Context, string) (io.ReadWriteCloser, []byte, error)

// OpenAgent authenticates and opens the guest-agent channel for a machine.
func OpenAgent(ctx context.Context, machineID string, connector GuestAgentConnector) (api.GuestAgent, error) {
	if connector == nil {
		return nil, errors.New("vmm: guest agent channel is not configured")
	}
	conn, key, err := connector(ctx, machineID)
	if err != nil {
		return nil, err
	}
	agent, err := guesttransport.NewAgent(conn, key)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return agent, nil
}

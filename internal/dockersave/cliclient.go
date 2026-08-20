package dockersave

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// cliRuntimeClient implements RuntimeClient by shelling out to the
// docker/podman CLI binary, the same way this package worked before the
// socket-based SocketClient was introduced. It exists as a fallback for
// NewRuntimeClientForName: most docker/podman installs do not run a
// background API service by default (docker's dockerd usually does;
// podman is normally daemonless for CLI use unless something has
// explicitly started `podman system service`/enabled the `podman.socket`
// systemd unit), so requiring a reachable socket unconditionally would
// regress every environment that has the CLI binary but no running
// socket service - confirmed by this exact failure in CI, where the
// socket path does not exist at all. The socket path stays the primary,
// preferred transport (it is the only one that works inside a
// distroless/scratch image with no CLI binary, e.g. platform-factory-mcp
// - see socketclient.go's doc comment); this is only reached when the
// socket is not reachable.
type cliRuntimeClient struct {
	runtimeName string
}

// LoadArchive implements RuntimeClient.
func (c *cliRuntimeClient) LoadArchive(ctx context.Context, body io.Reader) error {
	cmd := exec.CommandContext(ctx, c.runtimeName, "load")
	cmd.Stdin = body
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := stderr.String()
		if message == "" {
			return fmt.Errorf("%s load: %w", c.runtimeName, err)
		}
		return fmt.Errorf("%s load: %w: %s", c.runtimeName, err, message)
	}
	return nil
}

// ImageExists implements RuntimeClient. Mirrors this package's own
// pre-socket-client behavior: podman's `image exists` reports presence
// through its exit code alone (0 = present, 1 = absent), while docker
// has no equivalent subcommand and `image inspect` is used instead
// (0 = present, non-zero = absent).
func (c *cliRuntimeClient) ImageExists(ctx context.Context, reference string) (bool, error) {
	var args []string
	if c.runtimeName == "podman" {
		args = []string{"image", "exists", reference}
	} else {
		args = []string{"image", "inspect", reference}
	}
	cmd := exec.CommandContext(ctx, c.runtimeName, args...)
	if err := cmd.Run(); err != nil {
		return false, nil
	}
	return true, nil
}

package main

import (
	"context"
	"io"
	"os"

	"github.com/CYPT71/platform-factory/internal/dockersave"
)

func localLayoutPath(value string) bool {
	info, err := os.Stat(value)
	return err == nil && info.IsDir()
}

// prepareContainerImage backs `platform-factory import` and `platform-
// factory run`'s local-layout path - both load a layout into the local
// container runtime and never push it anywhere. The actual layout
// verification, image-reference resolution, and docker/podman streaming
// live in internal/dockersave, over a real HTTP connection to that
// runtime's own Unix domain socket rather than a shelled-out CLI
// process (see dockersave.SocketClient) - this is only the CLI-layer
// adapter that resolves which socket to dial for runtimeName.
func prepareContainerImage(ctx context.Context, runtimeName, image, layoutName string, stderr io.Writer) (string, error) {
	client, err := dockersave.NewRuntimeClientForName(runtimeName)
	if err != nil {
		return "", err
	}
	return dockersave.PrepareContainerImage(ctx, runtimeName, image, layoutName, stderr, client)
}

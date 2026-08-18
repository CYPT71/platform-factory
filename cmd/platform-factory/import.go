package main

import (
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
// live in internal/dockersave; this is only the CLI-layer adapter
// that hands it this process's own container-executing function.
func prepareContainerImage(runtimeName, image, layoutName string, stderr io.Writer, execute containerExecutor) (string, error) {
	return dockersave.PrepareContainerImage(runtimeName, image, layoutName, stderr, dockersave.Executor(execute))
}

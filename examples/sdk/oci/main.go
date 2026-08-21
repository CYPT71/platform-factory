// Command oci-sdk-example builds a secure OCI image by consuming sdk/oci
// directly, then shows both ways the resulting image can run: as an
// ordinary container (no secure-oci code involved at all) or, opt-in, under
// the secure-oci MicroVM runtime (sdk/microvm). Building never depends on
// which of the two the caller ends up choosing.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	microvm "github.com/CYPT71/platform-factory/sdk/microvm"
	oci "github.com/CYPT71/platform-factory/sdk/oci"
)

func main() {
	dir, err := os.MkdirTemp("", "platform-factory-oci-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// A trivial placeholder in place of a real, already-built application
	// binary. Production callers point Binary at a real statically linked
	// executable; the SDK does not build application code, only the image
	// around it.
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho hello from secure-oci\n"), 0o755); err != nil {
		log.Fatal(err)
	}

	layout := filepath.Join(dir, "image")
	digest, err := oci.Build(oci.Options{
		Binary: binary, Output: layout, Architecture: "amd64",
		ImageName: "example/service", Tag: "v1",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("built OCI image layout at %s\nmanifest digest: %s\n\n", layout, digest)

	// Mode 1: without the MicroVM - the layout is already a complete,
	// standard OCI Image Layout. Any OCI-compatible engine (Docker, Podman,
	// containerd) loads it directly; no further secure-oci API call is
	// needed for this path.
	fmt.Println("without the MicroVM: load the layout directly, e.g.")
	fmt.Printf("  skopeo copy oci:%s docker-daemon:example/service:v1\n\n", layout)

	// Mode 2: with the MicroVM - the same layout, unmodified, becomes the
	// boot source for a platform-factory-runtime MicroVM. This only validates the
	// specification; it does not require KVM/HVF and does not boot a real
	// machine, matching examples/sdk/microvm.
	spec := microvm.Spec{
		Name: "example-service", Layout: layout, Arch: "amd64",
		Listen: "127.0.0.1", MemoryMiB: 256, VCPUs: 1, Port: 8080,
	}
	if err := spec.ValidateCommon(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("with the MicroVM: the same layout also validates as a MicroVM boot source, e.g.")
	fmt.Printf("  platform-factory microvm run --layout %s --arch %s\n", layout, spec.Arch)
}

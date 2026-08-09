package oci_test

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	oci "github.com/CYPT71/secure-oci-base/sdk/oci"
)

func ExampleBuild() {
	dir, err := os.MkdirTemp("", "secure-oci-sdk-oci-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		log.Fatal(err)
	}

	layout := filepath.Join(dir, "image")
	digest, err := oci.Build(oci.Options{
		Binary: binary, Output: layout, Architecture: "amd64", ImageName: "example/service", Tag: "v1",
	})
	if err != nil {
		log.Fatal(err)
	}

	// The result is a standard OCI Image Layout: no secure-oci code is
	// needed to consume it, only to build it. `oci-layout` and `index.json`
	// are the two files every OCI-compatible engine looks for.
	for _, name := range []string{"oci-layout", "index.json"} {
		if _, err := os.Stat(filepath.Join(layout, name)); err != nil {
			log.Fatalf("missing %s: %v", name, err)
		}
	}

	fmt.Println(strings.HasPrefix(digest, "sha256:"))
	// Output:
	// true
}

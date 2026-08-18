// hello-pipeline is a minimal, stdlib-only program used as a fixture for
// the api/v1alpha1 pipeline system (see ../../pipeline.json). Execute a
// pipeline with "secure-oci pipeline run". It is not part of the legacy
// examples/ configuration system.
package main

import (
	"fmt"
	"os"
)

func greeting(name string) string {
	if name == "" {
		name = "world"
	}
	return fmt.Sprintf("hello, %s, from the secure-oci pipeline system", name)
}

func main() {
	fmt.Println(greeting(os.Getenv("HELLO_PIPELINE_NAME")))
}

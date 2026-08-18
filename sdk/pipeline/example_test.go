package pipeline_test

import (
	"fmt"
	"log"
	"strings"

	sdk "github.com/CYPT71/platform-factory/sdk/pipeline"
)

func ExampleDecode() {
	definition, graph, err := sdk.Decode(strings.NewReader(`{
		"api_version":"secure-oci.dev/v1","name":"hello-sdk",
		"stages":[{"id":"build","command":{"executable":"/bin/build"}}]
	}`))
	if err != nil {
		log.Fatal(err)
	}
	fingerprint, err := sdk.Fingerprint(definition)
	if err != nil {
		log.Fatal(err)
	}
	// Use sdk types for type assertions
	var _ sdk.Issue
	var _ sdk.ValidationError
	var _ sdk.Graph
	fmt.Println(definition.Name)
	fmt.Println(graph.Order)
	fmt.Println(strings.HasPrefix(fingerprint, "sha256:"))
	// Output: hello-sdk
	// [build]
	// true
}

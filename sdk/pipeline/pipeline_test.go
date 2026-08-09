package pipeline_test

import (
	"errors"
	"strings"
	"testing"

	pipeline "github.com/CYPT71/secure-oci-base/api/pipeline"
	v1 "github.com/CYPT71/secure-oci-base/api/v1"
	public "github.com/CYPT71/secure-oci-base/sdk/pipeline"
)

func TestExternalConsumerCanDecodeStablePipeline(t *testing.T) {
	definition, graph, err := pipeline.Decode(strings.NewReader(`{
		"api_version":"` + v1.APIVersion + `","name":"sdk-example",
		"stages":[{"id":"build","command":{"executable":"/bin/build"}},
		{"id":"package","depends_on":["build"],"command":{"executable":"/bin/package"}}]
	}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if definition.APIVersion != v1.APIVersion || definition.Name != "sdk-example" {
		t.Fatalf("unexpected definition: %#v", definition)
	}
	if got := strings.Join(graph.Order, ","); got != "build,package" {
		t.Fatalf("order = %q", got)
	}
}

// TestExternalConsumerCanDecodeLegacyStablePipeline proves the pre-rebrand
// secure-oci.dev/v1 wire identifier still decodes during the documented
// compatibility overlap window (docs/api-compatibility.md) - the decoded
// field holds exactly what was on the wire, not the current constant, so
// this compares against v1.LegacyAPIVersion rather than v1.APIVersion.
func TestExternalConsumerCanDecodeLegacyStablePipeline(t *testing.T) {
	definition, _, err := pipeline.Decode(strings.NewReader(`{
		"api_version":"` + v1.LegacyAPIVersion + `","name":"sdk-example",
		"stages":[{"id":"build","command":{"executable":"/bin/build"}}]
	}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if definition.APIVersion != v1.LegacyAPIVersion {
		t.Fatalf("unexpected definition: %#v", definition)
	}
}

func TestExternalConsumerGetsTypedValidationErrors(t *testing.T) {
	_, _, err := pipeline.Decode(strings.NewReader(`{
		"api_version":"secure-oci.dev/v1","name":"sdk-example",
		"stages":[{"id":"build","depends_on":["missing"],"command":{"executable":"/bin/build"}}
		]
	}`))
	var validation *public.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T %v, want *pipeline.ValidationError", err, err)
	}
	if len(validation.Issues) != 1 || validation.Issues[0].Path != "stages[0].depends_on[0]" {
		t.Fatalf("issues = %#v", validation.Issues)
	}
}

func TestExternalConsumerCanFingerprintWithoutMutation(t *testing.T) {
	definition := v1.Pipeline{APIVersion: v1.APIVersion, Name: "sdk-example", Stages: []v1.Stage{{
		ID: "build", Command: v1.Command{Executable: "/bin/build"}, Env: map[string]string{"B": "2", "A": "1"},
	}}}
	first, err := pipeline.Fingerprint(definition)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	second, err := pipeline.Fingerprint(definition)
	if err != nil {
		t.Fatalf("Fingerprint again: %v", err)
	}
	if first != second || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("fingerprints = %q, %q", first, second)
	}
	if definition.Stages[0].Network != "" {
		t.Fatal("Fingerprint mutated the caller's definition")
	}
}

func TestExternalConsumerCanAnalyzeAndCanonicalize(t *testing.T) {
	definition := v1.Pipeline{APIVersion: v1.APIVersion, Name: "sdk-example", Stages: []v1.Stage{{
		ID: "build", Command: v1.Command{Executable: "/bin/build"},
	}}}
	graph, err := pipeline.Analyze(definition)
	if err != nil || len(graph.Order) != 1 || graph.Order[0] != "build" {
		t.Fatalf("Analyze() graph=%+v err=%v", graph, err)
	}
	canonical, err := pipeline.CanonicalJSON(definition)
	if err != nil || !strings.Contains(string(canonical), `"name":"sdk-example"`) {
		t.Fatalf("CanonicalJSON()=%s err=%v", canonical, err)
	}
}

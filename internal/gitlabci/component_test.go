package gitlabci

import (
	"bytes"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestComponentHasPinnedBuildAndProtectedNativeBoot(t *testing.T) {
	data, err := Assets.ReadFile("assets/component.yml")
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var spec, jobs map[string]any
	if err := decoder.Decode(&spec); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&jobs); err != nil {
		t.Fatal(err)
	}
	inputs := spec["spec"].(map[string]any)["inputs"].(map[string]any)
	builder := inputs["builder-image"].(map[string]any)
	if builder["regex"] != "^.+@sha256:[a-f0-9]{64}$" || builder["default"] != nil {
		t.Fatal("builder image must have an explicit digest")
	}
	for _, name := range []string{"pf-build", "pf-package", "pf-boot"} {
		job := jobs[name].(map[string]any)
		if job["allow_failure"] != nil {
			t.Fatal("required gate made optional")
		}
		if job["script"] == nil {
			t.Fatal("missing executable phase")
		}
	}
	boot := jobs["pf-boot"].(map[string]any)
	rules := boot["rules"].([]any)
	if rules[0].(map[string]any)["if"] != `$CI_COMMIT_REF_PROTECTED == "true" && $CI_PIPELINE_SOURCE != "merge_request_event"` {
		t.Fatal("native boot must require protected refs")
	}
	if boot["image"] != nil {
		t.Fatal("native boot must use administrator-installed tools")
	}
}

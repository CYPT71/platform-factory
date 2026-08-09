package examples_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/CYPT71/secure-oci-base/conformance"
)

func TestLanguagePluginExamplesUseTheSameConformanceSuite(t *testing.T) {
	for name, fixture := range map[string]struct {
		interpreter string
		path        string
	}{
		"python":     {interpreter: "python3", path: "plugin-python/plugin.py"},
		"javascript": {interpreter: "node", path: "plugin-javascript/plugin.js"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath(fixture.interpreter); err != nil {
				t.Skipf("%s is not installed", fixture.interpreter)
			}
			results, err := conformance.RunPlugin(context.Background(), examplesPath("sdk", fixture.path))
			if err != nil {
				t.Fatal(err)
			}
			for _, result := range results {
				if !result.Passed {
					t.Errorf("%s: %s", result.Name, result.Detail)
				}
			}
		})
	}
}

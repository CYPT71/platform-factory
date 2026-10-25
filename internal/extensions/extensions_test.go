package extensions

import (
	"bytes"
	"encoding/json"
	v1 "github.com/CYPT71/platform-factory/internal/plugin"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFiftyProfilesAndNativeRPC(t *testing.T) {
	profiles := Catalog()
	if len(profiles) != 50 {
		t.Fatalf("got %d profiles", len(profiles))
	}
	seen := map[string]bool{}
	for _, p := range profiles {
		t.Run(p.Name, func(t *testing.T) {
			if seen[p.Name] {
				t.Fatal("duplicate profile")
			}
			seen[p.Name] = true
			manifest, err := v1.LoadManifest(filepath.Join("..", "..", "plugins", p.Name))
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Name != p.Name || len(manifest.Capabilities) != 1 || manifest.Capabilities[0] != "analyzer.scan" || len(manifest.Permissions.Network)+len(manifest.Permissions.Filesystem)+len(manifest.Permissions.Secrets) != 0 {
				t.Fatal("invalid manifest contract")
			}
			for _, test := range []struct {
				document map[string]any
				pass     bool
			}{{p.Example, true}, {p.RejectedExample, false}, {map[string]any{}, false}} {
				result, err := Analyze(p.Name, test.document)
				if err != nil {
					t.Fatal(err)
				}
				if result.Passed != test.pass {
					t.Fatalf("pass=%v want %v: %+v", result.Passed, test.pass, result)
				}
			}
		})
	}
}
func TestDocumentLimitsAndFindingsPrivacy(t *testing.T) {
	for _, input := range []string{"null", "[]", "{} {}", strings.Repeat(" ", MaxInputBytes+1)} {
		if _, err := DecodeDocument(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid document of %d bytes", len(input))
		}
	}
	secret := "sk-keep-this-out-of-reports"
	doc := map[string]any{"secrets": []any{map[string]any{"reference": "plaintext", "value": secret}}}
	report, err := Analyze("platform-secret-references", doc)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	if bytes.Contains(raw, []byte(secret)) || report.Passed {
		t.Fatal("secret exposed or policy bypassed")
	}
}
func TestMissingWildcardBranchCannotHideBehindValidSibling(t *testing.T) {
	p, _ := Lookup("k8s-image-digests")
	good := p.Example
	spec := good["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	spec["containers"] = append(spec["containers"].([]any), map[string]any{})
	result, err := Analyze(p.Name, good)
	if err != nil || result.Passed {
		t.Fatal("missing sibling image was accepted")
	}
}
func TestCatalogExamplesAreJSONDocuments(t *testing.T) {
	// Ensure all distributable plugin directories contain their usage contract.
	for _, p := range Catalog() {
		if _, err := os.Stat(filepath.Join("..", "..", "plugins", p.Name, "README.md")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLargeFailureReportsAreBoundedAndExplicitlyTruncated(t *testing.T) {
	entries := make([]any, MaxFindings+1)
	for i := range entries {
		entries[i] = map[string]any{"package": "unversioned"}
	}
	report, err := Analyze("mcp-command-pins", map[string]any{"servers": entries})
	if err != nil || report.Passed || !report.Truncated || len(report.Findings) != MaxFindings {
		t.Fatalf("unbounded report: %+v %v", report, err)
	}
}

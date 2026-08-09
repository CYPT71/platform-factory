package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestCompatFixturesRemainValid decodes and analyzes real, on-disk pipeline
// definitions previously accepted under a shipped api_version. Unlike
// validPipeline() elsewhere in this package, these fixtures cross the actual
// JSON boundary: encoding/json silently ignores fields it no longer
// recognizes, so a field rename in api/v1alpha1 or api/v1beta1, or a
// tightened validation rule, could otherwise "succeed" on an emptied-out
// pipeline without ever failing a build-time test. A fixture must never be
// edited to keep this test passing - a fixture failing here is a real
// backward-compatibility break and must be fixed in code, not in testdata.
func TestCompatFixturesRemainValid(t *testing.T) {
	entries, err := os.ReadDir("testdata/compat")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no compatibility fixtures found")
	}
	for _, entry := range entries {
		entry := entry
		t.Run(entry.Name(), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata/compat", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			_, graph, err := Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("decode and analyze: %v", err)
			}
			want, ok := compatExpectedOrder[entry.Name()]
			if !ok {
				t.Fatalf("fixture %q has no expected order registered in compatExpectedOrder", entry.Name())
			}
			if !reflect.DeepEqual(graph.Order, want) {
				t.Fatalf("order=%v want=%v (a field was silently dropped or reordered)", graph.Order, want)
			}
		})
	}
}

// compatExpectedOrder pins the exact topological order each fixture in
// testdata/compat must still produce, so a fixture that decodes without
// error but loses a dependency edge (a common silent-breakage shape) is
// still caught.
var compatExpectedOrder = map[string][]string{
	"v1alpha1-full.json":    {"assets", "compile", "package"},
	"v1alpha1-minimal.json": {"build"},
	"v1beta1-promoted.json": {"assets", "compile", "package"},
	"v1-stable.json":        {"assets", "compile", "package"},
}

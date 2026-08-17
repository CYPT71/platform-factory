package pipeline

import (
	"bytes"
	"os"
	"testing"
)

// FuzzDecode feeds arbitrary bytes at the pipeline document parser. A
// pipeline JSON is attacker-controlled by construction (whatever a CI job
// or a user hands `platform-factory pipeline plan/run`), and Decode/DAG
// validation must never panic on it, only return an error.
func FuzzDecode(f *testing.F) {
	if real, err := os.ReadFile("../../examples/pipeline.json"); err == nil {
		f.Add(real)
	}
	f.Add([]byte(`{"api_version":"platform-factory.dev/v1alpha1","name":"x","stages":[]}`))
	f.Add([]byte(`{"stages":[{"id":"a","depends_on":["a"]}]}`))
	f.Add([]byte(`{"stages":[{"id":"a","depends_on":["b"]},{"id":"b","depends_on":["a"]}]}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _, _ = Decode(bytes.NewReader(data))
	})
}

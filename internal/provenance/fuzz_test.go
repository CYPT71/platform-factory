package provenance

import (
	"strings"
	"testing"
)

// FuzzFromJournal feeds arbitrary bytes at the pipeline result journal
// decoder - the input to `secure-oci publish --journal PATH` and to
// verify-release's SLSA predicate generation. Untrusted by construction:
// a journal is a file on disk that could come from anywhere. secretField
// recurses through arbitrary map[string]any/[]any nesting depth with no
// explicit bound, the one part of this decode path most likely to have a
// pathological case (deep nesting, cycles are impossible from JSON but
// very wide/deep structures are not) that a fixed unit test wouldn't
// think to construct.
func FuzzFromJournal(f *testing.F) {
	f.Add(`{
	  "api_version":"secure-oci.dev/journal/v1",
	  "pipeline_fingerprint":"sha256:abc",
	  "engine_version":"secure-oci/1",
	  "sandbox":"on",
	  "generated":"2026-07-28T12:00:00Z",
	  "stages":[{"id":"build","state":"succeeded"}]
	}`)
	f.Add(`{"api_version":"secure-oci.dev/journal/v1","pipeline_fingerprint":"x","password":"leaked"}`)
	f.Add(`{}`)
	f.Add(`null`)
	f.Add(``)
	f.Add(`{"api_version":"secure-oci.dev/journal/v1","pipeline_fingerprint":"x","stages":[[[[[[[[[[]]]]]]]]]]]}`)
	// A wide, moderately deep structure - secretField has no explicit
	// recursion-depth or fan-out bound, so this is the shape most likely
	// to reveal a real cost-of-recursion problem, not just a decode error.
	deep := `{"api_version":"secure-oci.dev/journal/v1","pipeline_fingerprint":"x","stages":` +
		strings.Repeat(`[`, 200) + strings.Repeat(`]`, 200) + `}`
	f.Add(deep)

	f.Fuzz(func(t *testing.T, journal string) {
		if len(journal) > 1<<16 {
			t.Skip()
		}
		_, _ = FromJournal(strings.NewReader(journal), "https://secure-oci.dev/builder/v1")
	})
}

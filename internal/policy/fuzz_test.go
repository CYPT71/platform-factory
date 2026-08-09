package policy

import (
	"encoding/json"
	"testing"
)

// FuzzEvaluate feeds arbitrary Rules/Evidence JSON pairs into Evaluate.
// Rules and Evidence are decoded from operator/CI-supplied files
// (secure-oci publish --policy/--evidence, verify-release --policy/
// --evidence); Evaluate must never panic on adversarial input, only
// return an error or a Decision.
func FuzzEvaluate(f *testing.F) {
	f.Add(`{"api_version":"secure-oci.dev/policy/v1","require_sbom":true}`, `{"sbom":true,"subject_digest":"sha256:00"}`)
	f.Add(`{"api_version":"wrong"}`, `{}`)
	f.Add(`{}`, `{}`)
	f.Add(`null`, `null`)
	f.Add(`{"api_version":"secure-oci.dev/policy/v1","require_pins":true,"require_hardening":true,"require_sbom":true,"require_provenance":true,"require_signature":true,"require_reproducible":true}`, `{}`)

	f.Fuzz(func(t *testing.T, rulesJSON, evidenceJSON string) {
		if len(rulesJSON) > 1<<16 || len(evidenceJSON) > 1<<16 {
			t.Skip()
		}
		var rules Rules
		if json.Unmarshal([]byte(rulesJSON), &rules) != nil {
			return
		}
		var evidence Evidence
		if json.Unmarshal([]byte(evidenceJSON), &evidence) != nil {
			return
		}
		_, _ = Evaluate(rules, evidence)
	})
}

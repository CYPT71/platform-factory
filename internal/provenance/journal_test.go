package provenance

import (
	"strings"
	"testing"
)

func TestFromJournalProducesSLSAAndRejectsSecrets(t *testing.T) {
	journal := `{
	  "api_version":"secure-oci.dev/journal/v1",
	  "pipeline_fingerprint":"sha256:abc",
	  "engine_version":"secure-oci/1",
	  "sandbox":"on",
	  "generated":"2026-07-28T12:00:00Z",
	  "stages":[{"id":"build","state":"succeeded"}]
	}`
	predicate, err := FromJournal(strings.NewReader(journal), "https://secure-oci.dev/builder/v1")
	if err != nil {
		t.Fatal(err)
	}
	if predicate.BuildDefinition.BuildType == "" || predicate.RunDetails.Metadata.InvocationID != "sha256:abc" {
		t.Fatalf("predicate=%+v", predicate)
	}
	leaked := strings.Replace(journal, `"state":"succeeded"`, `"password":"leaked"`, 1)
	if _, err := FromJournal(strings.NewReader(leaked), "builder"); err == nil {
		t.Fatal("journal containing a password field was accepted")
	}
}

// TestFromJournalAcceptsTheCurrentAPIVersion proves the post-rebrand
// identifier itself converts, not only the legacy secure-oci.dev spelling
// exercised above - the two are easy to conflate since both decode through
// the same equality check in FromJournal.
func TestFromJournalAcceptsTheCurrentAPIVersion(t *testing.T) {
	journal := `{
	  "api_version":"platform-factory.dev/journal/v1",
	  "pipeline_fingerprint":"sha256:def",
	  "engine_version":"platform-factory/1",
	  "sandbox":"on",
	  "generated":"2026-07-28T12:00:00Z",
	  "stages":[{"id":"build","state":"succeeded"}]
	}`
	predicate, err := FromJournal(strings.NewReader(journal), "https://platform-factory.dev/builder/v1")
	if err != nil {
		t.Fatal(err)
	}
	if predicate.RunDetails.Metadata.InvocationID != "sha256:def" {
		t.Fatalf("predicate=%+v", predicate)
	}
}

// TestFromJournalRejectsAnUnknownAPIVersion proves a journal stamped with
// neither accepted spelling - not a typo of one of them, a genuinely
// foreign or future version - fails closed instead of being silently
// treated as provenance evidence.
func TestFromJournalRejectsAnUnknownAPIVersion(t *testing.T) {
	journal := `{
	  "api_version":"platform-factory.dev/journal/v99",
	  "pipeline_fingerprint":"sha256:abc",
	  "stages":[{"id":"build","state":"succeeded"}]
	}`
	if _, err := FromJournal(strings.NewReader(journal), "builder"); err == nil || !strings.Contains(err.Error(), "unsupported journal api_version") {
		t.Fatalf("err = %v", err)
	}
}

package provenance

import (
	"strings"
	"testing"
)

func TestFromJournalProducesSLSAAndRejectsSecrets(t *testing.T) {
	journal := `{
	  "api_version":"platform-factory.dev/journal/v1",
	  "pipeline_fingerprint":"sha256:abc",
	  "engine_version":"platform-factory/1",
	  "sandbox":"on",
	  "generated":"2026-07-28T12:00:00Z",
	  "stages":[{"id":"build","state":"succeeded"}]
	}`
	predicate, err := FromJournal(strings.NewReader(journal), "https://platform-factory.dev/builder/v1")
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

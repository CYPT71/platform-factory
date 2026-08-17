package provenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const SLSAPredicateType = "https://slsa.dev/provenance/v1"

type JournalPredicate struct {
	BuildDefinition struct {
		BuildType            string         `json:"buildType"`
		ExternalParameters   map[string]any `json:"externalParameters"`
		ResolvedDependencies []any          `json:"resolvedDependencies"`
	} `json:"buildDefinition"`
	RunDetails struct {
		Builder struct {
			ID string `json:"id"`
		} `json:"builder"`
		Metadata struct {
			InvocationID string    `json:"invocationId"`
			StartedOn    time.Time `json:"startedOn,omitempty"`
			FinishedOn   time.Time `json:"finishedOn,omitempty"`
		} `json:"metadata"`
	} `json:"runDetails"`
}

// FromJournal converts the actual pipeline journal into a SLSA v1 predicate.
// It fails closed if secret-like value fields occur anywhere in the journal.
func FromJournal(reader io.Reader, builderID string) (JournalPredicate, error) {
	if builderID == "" {
		return JournalPredicate{}, errors.New("provenance: builder identity is required")
	}
	var journal map[string]any
	decoder := json.NewDecoder(io.LimitReader(reader, 16<<20))
	if err := decoder.Decode(&journal); err != nil {
		return JournalPredicate{}, fmt.Errorf("provenance: decode journal: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return JournalPredicate{}, errors.New("provenance: journal must contain exactly one JSON object")
	}
	// secure-oci.dev/journal/v1 is the pre-rebrand identifier, still
	// accepted for the documented compatibility overlap window (see
	// docs/api-compatibility.md) - a journal retained as provenance
	// evidence may have been written well before this rename.
	if v := journal["api_version"]; v != "platform-factory.dev/journal/v1" && v != "secure-oci.dev/journal/v1" {
		return JournalPredicate{}, errors.New("provenance: unsupported journal api_version")
	}
	fingerprint, _ := journal["pipeline_fingerprint"].(string)
	if fingerprint == "" {
		return JournalPredicate{}, errors.New("provenance: journal has no pipeline fingerprint")
	}
	if field, found := secretField(journal, "journal"); found {
		return JournalPredicate{}, fmt.Errorf("provenance: secret-like field %s is forbidden", field)
	}
	var predicate JournalPredicate
	predicate.BuildDefinition.BuildType = "https://platform-factory.dev/build/pipeline/v1"
	predicate.BuildDefinition.ExternalParameters = map[string]any{
		"pipeline_fingerprint": fingerprint,
		"engine_version":       journal["engine_version"],
		"sandbox":              journal["sandbox"],
		"stages":               journal["stages"],
	}
	predicate.BuildDefinition.ResolvedDependencies = []any{}
	predicate.RunDetails.Builder.ID = builderID
	predicate.RunDetails.Metadata.InvocationID = fingerprint
	if generated, _ := journal["generated"].(string); generated != "" {
		if parsed, err := time.Parse(time.RFC3339, generated); err == nil {
			predicate.RunDetails.Metadata.FinishedOn = parsed
		}
	}
	return predicate, nil
}

func secretField(value any, path string) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			for _, forbidden := range []string{"secret_value", "password", "access_token", "private_key"} {
				if strings.Contains(lower, forbidden) {
					return path + "." + key, true
				}
			}
			if found, ok := secretField(child, path+"."+key); ok {
				return found, true
			}
		}
	case []any:
		for index, child := range typed {
			if found, ok := secretField(child, fmt.Sprintf("%s[%d]", path, index)); ok {
				return found, true
			}
		}
	}
	return "", false
}

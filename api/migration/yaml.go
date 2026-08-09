package migration

import (
	"bytes"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

const MaxCanonicalYAMLBytes = 16 << 20

// MarshalYAML emits stable YAML. Map keys are sorted by yaml.v3 and canonical
// plans are sorted without mutating the caller.
func MarshalYAML(v interface{}) ([]byte, error) {
	if p, ok := v.(*MigrationPlan); ok {
		canonical := p.Canonical()
		canonical.Digest = p.Digest
		return yaml.Marshal(canonical)
	}
	return yaml.Marshal(v)
}

// UnmarshalYAML rejects unknown fields so format drift cannot silently erase
// security- or correctness-relevant observations.
func UnmarshalYAML(data []byte, v interface{}) error {
	if len(data) > MaxCanonicalYAMLBytes {
		return fmt.Errorf("decode canonical migration YAML: document exceeds %d bytes", MaxCanonicalYAMLBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("decode canonical migration YAML: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode canonical migration YAML: multiple documents are not allowed")
		}
		return fmt.Errorf("decode canonical migration YAML: trailing document: %w", err)
	}
	return nil
}

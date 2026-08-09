package project

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// CurrentConfigVersion is the newest project config schema this build
// understands. There is no v2 schema today; the migration framework
// exists so a future schema bump ships together with an executable
// migration instead of an opaque rejection, and so legacy documents
// without an explicit version can be normalized in place.
const CurrentConfigVersion = 1

// MigrationChange records one field-level rewrite applied by Migrate.
type MigrationChange struct {
	Field  string `json:"field"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// configMigrations maps a source schema version to the step migrating a
// config document to the next version. Each step mutates the YAML
// mapping node in place, which preserves unrelated fields, field order
// and comments.
var configMigrations = map[int]func(mapping *yaml.Node) ([]MigrationChange, error){
	0: migrateConfigV0ToV1,
}

// Migrate rewrites a raw project config document to
// CurrentConfigVersion and reports every change it applied. Documents
// already at the current version round-trip with no changes. Documents
// newer than CurrentConfigVersion fail: downgrading would have to
// invent semantics for fields this build has never seen.
func Migrate(raw []byte) ([]byte, []MigrationChange, error) {
	if len(raw) > 1<<20 {
		return nil, nil, errors.New("project config exceeds 1 MiB")
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, fmt.Errorf("decode project config: %w", err)
	}
	if err := decoder.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("project config must contain exactly one YAML/JSON document")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 ||
		document.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("project config must be a single mapping document")
	}
	mapping := document.Content[0]
	version, err := configDocumentVersion(mapping)
	if err != nil {
		return nil, nil, err
	}
	if version > CurrentConfigVersion {
		return nil, nil, fmt.Errorf("config version %d is newer than this platform-factory supports (max %d); upgrade platform-factory",
			version, CurrentConfigVersion)
	}
	var changes []MigrationChange
	for version < CurrentConfigVersion {
		step, registered := configMigrations[version]
		if !registered {
			return nil, nil, fmt.Errorf("no migration registered from config version %d", version)
		}
		applied, err := step(mapping)
		if err != nil {
			return nil, nil, fmt.Errorf("migrate config version %d: %w", version, err)
		}
		changes = append(changes, applied...)
		next, err := configDocumentVersion(mapping)
		if err != nil {
			return nil, nil, err
		}
		if next <= version {
			return nil, nil, fmt.Errorf("migration from config version %d did not advance the version", version)
		}
		version = next
	}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, nil, fmt.Errorf("encode migrated config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, fmt.Errorf("encode migrated config: %w", err)
	}
	return buffer.Bytes(), changes, nil
}

func configDocumentVersion(mapping *yaml.Node) (int, error) {
	value := configMappingValue(mapping, "version")
	if value == nil {
		return 0, nil
	}
	if value.Kind != yaml.ScalarNode {
		return 0, errors.New("version must be a non-negative integer")
	}
	version, err := strconv.Atoi(value.Value)
	if err != nil || version < 0 {
		return 0, errors.New("version must be a non-negative integer")
	}
	return version, nil
}

func configMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Kind == yaml.ScalarNode && mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

// migrateConfigV0ToV1 makes the historically implicit config version
// explicit. Version 0 never existed as a distinct schema: it is the
// same document shape with the version field absent (or written as 0),
// which Load used to default to 1 silently.
func migrateConfigV0ToV1(mapping *yaml.Node) ([]MigrationChange, error) {
	if value := configMappingValue(mapping, "version"); value != nil {
		from := value.Value
		value.Value, value.Tag = "1", "!!int"
		return []MigrationChange{{
			Field: "version", From: from, To: "1",
			Reason: "config version 0 is the legacy spelling of version 1",
		}}, nil
	}
	mapping.Content = append([]*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "version"},
		{Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"},
	}, mapping.Content...)
	return []MigrationChange{{
		Field: "version", From: "", To: "1",
		Reason: "make the implicit config version explicit",
	}}, nil
}

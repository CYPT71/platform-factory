package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type publishedSchema struct {
	Schema               string                     `json:"$schema"`
	AdditionalProperties bool                       `json:"additionalProperties"`
	Properties           map[string]json.RawMessage `json:"properties"`
}

type lockV1 struct {
	Version   int    `json:"version"`
	GitCommit string `json:"git_commit,omitempty"`
}

func TestPublishedSchemasMatchGoWireFields(t *testing.T) {
	root := filepath.Join("..", "..", "schemas")
	expectedDigests := map[string]string{"pf-v1.schema.json": "165181097a4f9691d38dcdbe1b73007f70c951cd4500c98283e2841c21c4d90c", "pf-lock-v1.schema.json": "930de3d533446b12c22ed7728d60afccad6b695631c52e1c32528c3fcdad376a"}
	for name, typ := range map[string]reflect.Type{"pf-v1.schema.json": reflect.TypeOf(Config{}), "pf-lock-v1.schema.json": reflect.TypeOf(lockV1{}), "pf-lock-v2.schema.json": reflect.TypeOf(Lock{})} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		var schema publishedSchema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if schema.Schema != "https://json-schema.org/draft/2020-12/schema" || schema.AdditionalProperties {
			t.Fatalf("%s is not a closed 2020-12 schema", name)
		}
		digest := sha256.Sum256(raw)
		if expected := expectedDigests[name]; expected != "" && hex.EncodeToString(digest[:]) != expected {
			got := hex.EncodeToString(digest[:])
			t.Fatalf("%s changed (%s): schema v1 is stable; publish a new schema version or intentionally update compatibility evidence", name, got)
		}
		var fields []string
		for index := 0; index < typ.NumField(); index++ {
			tag := typ.Field(index).Tag.Get("json")
			field := strings.Split(tag, ",")[0]
			if field != "" && field != "-" {
				fields = append(fields, field)
			}
		}
		var properties []string
		for property := range schema.Properties {
			properties = append(properties, property)
		}
		sort.Strings(fields)
		sort.Strings(properties)
		if !reflect.DeepEqual(fields, properties) {
			t.Fatalf("%s fields=%v schema=%v", name, fields, properties)
		}
	}
}

func TestPublishedSchemaFixturesUseProductReaders(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "pf.yaml")
	writeTestFile(t, valid, "version: 1\nlanguage: go\nartifact: app\nisolation: container\nruntime_engine: docker\nplatform: linux/amd64\n", 0o600)
	if _, err := Load(valid); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, valid, "version: 1\nlanguage: go\nartifact: app\nunknown_schema_field: true\n", 0o600)
	if _, err := Load(valid); err == nil {
		t.Fatal("unknown config field accepted")
	}
	lock := filepath.Join(dir, "pf.lock")
	writeTestFile(t, lock, `{"version":1,"git_commit":"abc123"}`, 0o600)
	if _, err := LoadLock(lock); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, lock, `{"version":1,"unknown":true}`, 0o600)
	if _, err := LoadLock(lock); err == nil {
		t.Fatal("unknown lock field accepted")
	}
}

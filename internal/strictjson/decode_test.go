package strictjson

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeAcceptsOneKnownValueOnly(t *testing.T) {
	type record struct {
		Name string `json:"name"`
	}
	var value record
	if err := Decode([]byte(`{"name":"ok"}`), &value); err != nil || value.Name != "ok" {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	for _, invalid := range []string{
		`{"name":"ok","unknown":true}`,
		`{"name":"ok"} {"name":"second"}`,
	} {
		if err := Decode([]byte(invalid), &value); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}

func TestDecodeFileReadsAndAppliesTheSameRules(t *testing.T) {
	type record struct {
		Name string `json:"name"`
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "value.json")
	if err := os.WriteFile(path, []byte(`{"name":"ok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var value record
	if err := DecodeFile(path, &value); err != nil || value.Name != "ok" {
		t.Fatalf("value=%+v err=%v", value, err)
	}

	if err := os.WriteFile(path, []byte(`{"name":"ok","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DecodeFile(path, &value); err == nil {
		t.Fatal("expected rejection of an unknown field")
	}

	if err := DecodeFile(filepath.Join(dir, "missing.json"), &value); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

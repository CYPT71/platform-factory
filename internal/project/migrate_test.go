package project

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func writeFileHelper(name string, data []byte) error {
	return os.WriteFile(name, data, 0o644)
}

func TestMigrateAddsImplicitVersion(t *testing.T) {
	raw := []byte("# build recipe\nlanguage: compiled\nartifact: app\n")
	migrated, changes, err := Migrate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Field != "version" || changes[0].To != "1" {
		t.Fatalf("changes=%+v", changes)
	}
	text := string(migrated)
	if !strings.Contains(text, "version: 1") || !strings.Contains(text, "# build recipe") {
		t.Fatalf("migrated=%s", text)
	}
	again, moreChanges, err := Migrate(migrated)
	if err != nil || len(moreChanges) != 0 || !bytes.Equal(again, migrated) {
		t.Fatalf("second migration changed the document: changes=%+v err=%v", moreChanges, err)
	}
}

func TestMigrateRewritesExplicitVersionZero(t *testing.T) {
	migrated, changes, err := Migrate([]byte("version: 0\nlanguage: compiled\nartifact: app\n"))
	if err != nil || len(changes) != 1 || changes[0].From != "0" {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	if !strings.Contains(string(migrated), "version: 1") {
		t.Fatalf("migrated=%s", migrated)
	}
}

func TestMigrateRejectsInvalidInput(t *testing.T) {
	for name, raw := range map[string]string{
		"newer":     "version: 3\nlanguage: compiled\nartifact: app\n",
		"malformed": "language: [unclosed\n",
		"sequence":  "- not\n- a\n- mapping\n",
		"documents": "language: compiled\n---\nlanguage: other\n",
		"badver":    "version: soon\nlanguage: compiled\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Migrate([]byte(raw)); err == nil {
				t.Fatal("expected migration error")
			}
		})
	}
	if _, _, err := Migrate(append(bytes.Repeat([]byte("#"), 1<<20), '\n')); err == nil {
		t.Fatal("expected oversized document rejection")
	}
}

func TestMigratePreservesUnrelatedFieldsAndComments(t *testing.T) {
	raw := []byte("# keep me\nlanguage: python\nruntime: bin/python\nenv:\n  MODE: prod\n")
	migrated, changes, err := Migrate(raw)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	text := string(migrated)
	for _, want := range []string{"# keep me", "language: python", "runtime: bin/python", "MODE: prod", "version: 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("migrated missing %q: %s", want, text)
		}
	}
	loaded, err := loadFromBytes(t, migrated)
	if err != nil || loaded.Config.Version != 1 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func loadFromBytes(t *testing.T, data []byte) (Loaded, error) {
	t.Helper()
	dir := t.TempDir()
	name := dir + "/.config_image.yaml"
	if err := writeFileHelper(name, data); err != nil {
		t.Fatal(err)
	}
	return Load(name)
}

func TestValidateDistinguishesNewerConfigVersions(t *testing.T) {
	loaded := Loaded{Config: Config{Version: 2, Language: "compiled", Artifact: "app",
		Isolation: "container", RuntimeEngine: "docker", Platform: "linux/amd64"}}
	err := loaded.Validate()
	if err == nil || !strings.Contains(err.Error(), "upgrade platform-factory") {
		t.Fatalf("err=%v", err)
	}
}

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	plugin "github.com/CYPT71/platform-factory/sdk/plugin"
)

func TestOfficialAdaptersCoverSupportedLanguages(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "requirements.txt"), []byte("example==1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, language := range []string{"go", "node", "python", "dotnet", "rust", "ruby", "php"} {
		raw, _ := json.Marshal(plugin.FreezeParams{Language: language, Root: root})
		value, err := handleFreeze(context.Background(), raw)
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		if len(value.(plugin.FreezeResult).Steps) == 0 {
			t.Fatalf("%s returned no steps", language)
		}
	}
	for name, language := range map[string]string{"pom.xml": "java"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("<project/>"), 0o644); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(plugin.FreezeParams{Language: language, Root: root})
		if _, err := handleFreeze(context.Background(), raw); err != nil {
			t.Fatalf("%s: %v", language, err)
		}
	}
}

func TestOfficialPlanNeverExecutesCommands(t *testing.T) {
	raw, _ := json.Marshal(plugin.PlanParams{Language: "go", Root: t.TempDir()})
	value, err := handlePlan(context.Background(), raw)
	if err != nil || len(value.(plugin.PlanResult).Notes) != 2 {
		t.Fatalf("value=%+v err=%v", value, err)
	}
}

func TestOfficialDetectAndValidationErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plugin.DetectParams{Path: root})
	value, err := handleDetect(context.Background(), raw)
	if err != nil || value.(plugin.DetectResult).Kind != "unknown" || len(value.(plugin.DetectResult).Evidence) == 0 {
		t.Fatalf("value=%+v err=%v", value, err)
	}
	for _, raw := range []json.RawMessage{[]byte(`{`), []byte(`{}`)} {
		if _, err := handleDetect(context.Background(), raw); err == nil {
			t.Fatalf("detect accepted %q", raw)
		}
	}
	if _, err := handleDetect(context.Background(), json.RawMessage(`{"path":"/does/not/exist"}`)); err == nil {
		t.Fatal("detect accepted missing path")
	}
	for _, raw := range []json.RawMessage{[]byte(`{`), []byte(`{}`)} {
		if _, err := handleFreeze(context.Background(), raw); err == nil {
			t.Fatalf("freeze accepted %q", raw)
		}
	}
	if _, err := handlePlan(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Fatal("plan accepted malformed params")
	}
	if _, err := handlePlan(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("plan accepted missing language")
	}
}

func TestOfficialFreezeSelectsLockfilesAndRejectsUnsupportedProjects(t *testing.T) {
	root := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("package-lock.json")
	raw, _ := json.Marshal(plugin.FreezeParams{Language: "node", Root: root})
	value, err := handleFreeze(context.Background(), raw)
	if err != nil || len(value.(plugin.FreezeResult).Steps) != 1 {
		t.Fatalf("node value=%+v err=%v", value, err)
	}
	write("mvnw")
	raw, _ = json.Marshal(plugin.FreezeParams{Language: "java", Root: root})
	value, err = handleFreeze(context.Background(), raw)
	if err != nil || value.(plugin.FreezeResult).Steps[0][0] != "./mvnw" {
		t.Fatalf("java value=%+v err=%v", value, err)
	}
	empty := t.TempDir()
	for _, language := range []string{"python", "java", "unknown"} {
		raw, _ = json.Marshal(plugin.FreezeParams{Language: language, Root: empty})
		if _, err := handleFreeze(context.Background(), raw); err == nil {
			t.Fatalf("%s unexpectedly supported", language)
		}
	}
}

package project

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRepo builds a tiny, self-contained git repo with a go.mod, an
// internal/widget package carrying a real doc comment, and a cmd/foo
// directory - enough for Inspect/GatherArchitecture to have real,
// on-disk truth to read rather than fixture-only fields.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("go.mod", "module example.com/fixture\n\ngo 1.25\n")
	mustWrite("internal/widget/widget.go", "// Package widget makes widgets.\npackage widget\n")
	mustWrite("cmd/foo/main.go", "package main\n\nfunc main() {}\n")

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "fixture@example.com")
	run("config", "user.name", "Fixture")
	run("config", "commit.gpgsign", "false")
	run("add", "-A")
	run("commit", "-m", "initial")
	return dir
}

func TestInspectSummaryOmitsComponents(t *testing.T) {
	dir := fixtureRepo(t)
	info, err := Inspect(context.Background(), dir, "1.2.3", "summary")
	if err != nil {
		t.Fatal(err)
	}
	if info.Module != "example.com/fixture" {
		t.Fatalf("module=%q", info.Module)
	}
	if info.Branch != "main" {
		t.Fatalf("branch=%q", info.Branch)
	}
	if info.Dirty {
		t.Fatal("expected a clean fixture repo")
	}
	if info.Components != nil {
		t.Fatalf("expected no components at summary depth, got %v", info.Components)
	}
	if len(info.ValidationCommands) == 0 {
		t.Fatal("expected validation commands to be populated")
	}
}

func TestInspectDetailedListsComponents(t *testing.T) {
	dir := fixtureRepo(t)
	info, err := Inspect(context.Background(), dir, "1.2.3", "detailed")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range info.Components {
		if c == "internal/widget" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected internal/widget among components, got %v", info.Components)
	}
}

func TestInspectDetectsADirtyWorkingTree(t *testing.T) {
	dir := fixtureRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(context.Background(), dir, "1.2.3", "summary")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Dirty {
		t.Fatal("expected Dirty=true with an untracked file present")
	}
}

func TestInspectToolHandlerRejectsAnInvalidDepth(t *testing.T) {
	dir := fixtureRepo(t)
	handler := InspectToolHandler(dir, "1.2.3")
	if _, err := handler(context.Background(), json.RawMessage(`{"depth":"bogus"}`)); err == nil {
		t.Fatal("expected an error for an invalid depth value")
	}
}

func TestInspectToolHandlerDefaultsToSummary(t *testing.T) {
	dir := fixtureRepo(t)
	handler := InspectToolHandler(dir, "1.2.3")
	text, err := handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var info Info
	if err := json.Unmarshal([]byte(text), &info); err != nil {
		t.Fatal(err)
	}
	if info.Components != nil {
		t.Fatalf("expected the default depth to omit components, got %v", info.Components)
	}
}

func TestProjectResourceHandlerReturnsDetailedJSON(t *testing.T) {
	dir := fixtureRepo(t)
	handler := ProjectResourceHandler(dir, "1.2.3")
	text, mimeType, err := handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "application/json" {
		t.Fatalf("mimeType=%q", mimeType)
	}
	if !strings.Contains(text, "internal/widget") {
		t.Fatalf("expected the detailed component list, got %s", text)
	}
}

func TestGatherArchitectureReadsRealPackageDocComments(t *testing.T) {
	dir := fixtureRepo(t)
	arch := GatherArchitecture(dir)
	if arch.Module != "example.com/fixture" {
		t.Fatalf("module=%q", arch.Module)
	}
	var widget *PackageSummary
	for i := range arch.Packages {
		if arch.Packages[i].Package == "internal/widget" {
			widget = &arch.Packages[i]
		}
	}
	if widget == nil {
		t.Fatalf("expected internal/widget in packages, got %v", arch.Packages)
	}
	if widget.Doc != "Package widget makes widgets." {
		t.Fatalf("doc=%q", widget.Doc)
	}
	found := false
	for _, c := range arch.Commands {
		if c == "foo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected cmd/foo among commands, got %v", arch.Commands)
	}
}

func TestArchitectureResourceHandlerReturnsJSON(t *testing.T) {
	dir := fixtureRepo(t)
	handler := ArchitectureResourceHandler(dir)
	text, mimeType, err := handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "application/json" {
		t.Fatalf("mimeType=%q", mimeType)
	}
	var arch Architecture
	if err := json.Unmarshal([]byte(text), &arch); err != nil {
		t.Fatal(err)
	}
}

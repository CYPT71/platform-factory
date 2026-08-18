package product

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestBuildToolHandlerRejectsMalformedJSON(t *testing.T) {
	_, err := BuildToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestBuildToolHandlerRejectsNULByteInExtraArgs(t *testing.T) {
	payload := json.RawMessage("{\"extra_args\":[\"a\\u0000b\"]}")
	_, err := BuildToolHandler(t.TempDir())(context.Background(), payload)
	if err == nil {
		t.Fatal("expected an error for a NUL byte in extra_args")
	}
}

func TestBuildToolHandlerRejectsPathTraversalInOutput(t *testing.T) {
	_, err := BuildToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"output":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal output")
	}
}

func TestBuildToolHandlerRejectsPathTraversalInConfig(t *testing.T) {
	_, err := BuildToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"config":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal config")
	}
}

// TestBuildToolHandlerBuildsProjectModeArgs exercises every flag
// build.go's buildArguments understands and checks the exact argv run()
// is handed, using the helper-process stand-in (see product_test.go) so
// the assertion is against real subprocess plumbing, not just the
// in-memory struct.
func TestBuildToolHandlerBuildsProjectModeArgs(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	payload := `{
		"dry_run": true,
		"output": "out/bin",
		"config": "pf.yaml",
		"arch": "arm64",
		"os": "linux",
		"platforms": ["linux/amd64", "linux/arm64"],
		"entrypoint": "main",
		"profile": "release",
		"image": "img",
		"tag": "v1",
		"format": "oci",
		"compression": "gzip",
		"semantic_layers": true,
		"rebuild": 3,
		"require_identical": true,
		"extra_files": ["a.txt", "b.txt"],
		"labels": ["k=v"],
		"extra_args": ["--custom-flag"]
	}`
	out, err := BuildToolHandler(repoRoot)(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON: %v\n%s", err, out)
	}
	if result.Command != "build" {
		t.Fatalf("command = %q, want %q", result.Command, "build")
	}
	want := []string{
		"--dry-run", "--output", "out/bin", "--config", "pf.yaml",
		"--arch", "arm64", "--os", "linux",
		"--platform", "linux/amd64", "--platform", "linux/arm64",
		"--entrypoint", "main", "--profile", "release",
		"--image", "img", "--tag", "v1", "--format", "oci",
		"--compression", "gzip", "--semantic-layers",
		"--rebuild", "3", "--require-identical",
		"--extra-file", "a.txt", "--extra-file", "b.txt",
		"--label", "k=v", "--custom-flag",
	}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

// TestBuildToolHandlerLowLevelModeAppendsExecutableLast checks the
// low-level (single-executable) mode's one distinguishing behavior: the
// executable argument is appended after every flag, never before.
func TestBuildToolHandlerLowLevelModeAppendsExecutableLast(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := BuildToolHandler(repoRoot)(context.Background(), json.RawMessage(`{"executable":"bin/app","tag":"v1"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	want := []string{"--tag", "v1", "bin/app"}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

func TestBuildToolHandlerOmitsUnsetFlags(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := BuildToolHandler(repoRoot)(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Args) != 0 {
		t.Fatalf("expected no args for an all-default payload, got %v", result.Args)
	}
}

package product

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestInitToolHandlerRejectsMalformedJSON(t *testing.T) {
	_, err := InitToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestInitToolHandlerRejectsNULByteInExtraArgs(t *testing.T) {
	payload := json.RawMessage("{\"extra_args\":[\"a\\u0000b\"]}")
	_, err := InitToolHandler(t.TempDir())(context.Background(), payload)
	if err == nil {
		t.Fatal("expected an error for a NUL byte in extra_args")
	}
}

func TestInitToolHandlerRejectsPathTraversalInExtractTo(t *testing.T) {
	_, err := InitToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"extract_to":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal extract_to")
	}
}

// TestInitToolHandlerBuildsFullArgs exercises every flag initArguments
// understands, in the exact order init.go appends them, via the
// helper-process stand-in so the assertion is against the real argv
// run() hands to the subprocess.
func TestInitToolHandlerBuildsFullArgs(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	payload := `{
		"directory": "project",
		"dry_run": true,
		"yes": true,
		"boot_disk": "disk.img",
		"language": "go",
		"artifact": "bin",
		"dependency_mode": "vendor",
		"runtime": "native",
		"engine": "docker",
		"build_command": "make",
		"build_args": ["X=1", "Y=2"],
		"extract_to": "out",
		"archive_format": "zip",
		"filename_style": "kebab",
		"extra_args": ["--extra1"]
	}`
	out, err := InitToolHandler(repoRoot)(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON: %v\n%s", err, out)
	}
	if result.Command != "init" {
		t.Fatalf("command = %q, want %q", result.Command, "init")
	}
	want := []string{
		"--dry-run", "--yes", "--boot-disk", "disk.img", "--language", "go",
		"--artifact", "bin", "--dependency-mode", "vendor", "--runtime", "native",
		"--engine", "docker", "--build-command", "make",
		"--build-arg", "X=1", "--build-arg", "Y=2",
		"--extract-to", "out", "--archive-format", "zip", "--filename-style", "kebab",
		"--extra1", "project",
	}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

// TestInitToolHandlerHonorsProjectRootOverride is F3's core assertion for
// pf_init: project_root, not the server's own repoRoot, becomes the
// subprocess's working directory when supplied - even though repoRoot
// itself must have a go.mod at startup, project_root is not required to.
func TestInitToolHandlerHonorsProjectRootOverride(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	independentProject := t.TempDir()
	payload := fmt.Sprintf(`{"project_root":%q,"language":"node"}`, independentProject)
	out, err := InitToolHandler(repoRoot)(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	gotCwd := cwdFromStdout(t, result.Stdout)
	if evalSymlinksOrFatal(t, gotCwd) != evalSymlinksOrFatal(t, independentProject) {
		t.Fatalf("subprocess ran in %q, want %q", gotCwd, independentProject)
	}
}

func TestInitToolHandlerRejectsARelativeProjectRoot(t *testing.T) {
	_, err := InitToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"project_root":"relative/path"}`))
	if err == nil {
		t.Fatal("expected an error for a relative project_root")
	}
}

func TestInitToolHandlerOmitsUnsetFlags(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := InitToolHandler(repoRoot)(context.Background(), json.RawMessage(`{}`))
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

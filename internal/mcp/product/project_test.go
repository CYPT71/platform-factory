package product

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestProjectToolHandlerRejectsMalformedJSON(t *testing.T) {
	_, err := ProjectToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestProjectToolHandlerRejectsNULByteInExtraArgs(t *testing.T) {
	payload := json.RawMessage("{\"action\":\"show\",\"extra_args\":[\"a\\u0000b\"]}")
	_, err := ProjectToolHandler(t.TempDir())(context.Background(), payload)
	if err == nil {
		t.Fatal("expected an error for a NUL byte in extra_args")
	}
}

func TestProjectToolHandlerRejectsPathTraversalInConfig(t *testing.T) {
	_, err := ProjectToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"action":"show","config":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal config")
	}
}

// TestProjectToolHandlerActionSpecificFlags checks that --dry-run,
// --max-wall-clock/--max-cpu/--max-memory, and --write are only ever
// threaded through for the actions project.go actually documents them
// for (freeze/build, build, and migrate respectively) - the same
// per-action gating validProjectActions/the handler's if-chain encodes.
func TestProjectToolHandlerActionSpecificFlags(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()

	cases := []struct {
		name    string
		payload string
		want    []string
	}{
		{
			name:    "show ignores dry_run and write",
			payload: `{"action":"show","dry_run":true,"write":true,"directory":"proj"}`,
			want:    []string{"show", "proj"},
		},
		{
			name:    "plan ignores dry_run",
			payload: `{"action":"plan","dry_run":true}`,
			want:    []string{"plan"},
		},
		{
			name:    "freeze honors dry_run",
			payload: `{"action":"freeze","dry_run":true,"config":"pf.yaml"}`,
			want:    []string{"freeze", "--config", "pf.yaml", "--dry-run"},
		},
		{
			name:    "build honors dry_run and resource limits",
			payload: `{"action":"build","dry_run":true,"max_wall_clock":"10m","max_cpu":"2","max_memory":"1Gi"}`,
			want:    []string{"build", "--dry-run", "--max-wall-clock", "10m", "--max-cpu", "2", "--max-memory", "1Gi"},
		},
		{
			name:    "run ignores build-only flags",
			payload: `{"action":"run"}`,
			want:    []string{"run"},
		},
		{
			name:    "launch ignores build-only flags",
			payload: `{"action":"launch"}`,
			want:    []string{"launch"},
		},
		{
			name:    "migrate honors write",
			payload: `{"action":"migrate","write":true}`,
			want:    []string{"migrate", "--write"},
		},
		{
			name:    "migrate without write omits the flag",
			payload: `{"action":"migrate"}`,
			want:    []string{"migrate"},
		},
	}

	for _, tc := range cases {
		out, err := ProjectToolHandler(repoRoot)(context.Background(), json.RawMessage(tc.payload))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
		var result Result
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("%s: expected valid JSON: %v\n%s", tc.name, err, out)
		}
		if result.Command != "project" {
			t.Fatalf("%s: command = %q, want %q", tc.name, result.Command, "project")
		}
		if !reflect.DeepEqual(result.Args, tc.want) {
			t.Fatalf("%s: args = %v\nwant  %v", tc.name, result.Args, tc.want)
		}
	}
}

// TestProjectToolHandlerHonorsProjectRootOverride is F3's core assertion
// for pf_project: project_root, not the server's own repoRoot, becomes the
// subprocess's working directory when supplied.
func TestProjectToolHandlerHonorsProjectRootOverride(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	independentProject := t.TempDir()
	payload := fmt.Sprintf(`{"action":"show","project_root":%q}`, independentProject)
	out, err := ProjectToolHandler(repoRoot)(context.Background(), json.RawMessage(payload))
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

func TestProjectToolHandlerRejectsARelativeProjectRoot(t *testing.T) {
	_, err := ProjectToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"action":"show","project_root":"relative/path"}`))
	if err == nil {
		t.Fatal("expected an error for a relative project_root")
	}
}

func TestProjectToolHandlerAppendsExtraArgsBeforeDirectory(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := ProjectToolHandler(repoRoot)(context.Background(), json.RawMessage(`{"action":"show","extra_args":["--verbose"],"directory":"proj"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	want := []string{"show", "--verbose", "proj"}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

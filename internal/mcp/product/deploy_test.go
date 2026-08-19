package product

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestDeployToolHandlerRejectsMalformedJSON(t *testing.T) {
	_, err := DeployToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestDeployToolHandlerRejectsNULByteInExtraArgs(t *testing.T) {
	payload := json.RawMessage("{\"extra_args\":[\"a\\u0000b\"]}")
	_, err := DeployToolHandler(t.TempDir())(context.Background(), payload)
	if err == nil {
		t.Fatal("expected an error for a NUL byte in extra_args")
	}
}

func TestDeployToolHandlerRejectsPathTraversalInReports(t *testing.T) {
	_, err := DeployToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"reports":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal reports directory")
	}
}

func TestDeployToolHandlerRejectsPathTraversalInPolicy(t *testing.T) {
	_, err := DeployToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"policy":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal policy")
	}
}

func TestDeployToolHandlerRejectsPathTraversalInEvidence(t *testing.T) {
	_, err := DeployToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"evidence":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal evidence directory")
	}
}

// TestDeployToolHandlerBuildsFullArgs exercises every flag
// deployArguments understands, in the exact order deploy.go appends
// them, via the helper-process stand-in so the assertion is against the
// real argv run() hands to the subprocess.
func TestDeployToolHandlerBuildsFullArgs(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	payload := `{
		"image": "img@sha256:abc",
		"name": "svc",
		"namespace": "ns",
		"replicas": 2,
		"port": 8080,
		"workload": "deployment",
		"schedule": "0 * * * *",
		"cpu_request": "100m",
		"memory_request": "128Mi",
		"runtime_class": "gvisor",
		"ingress_host": "example.com",
		"ingress_path": "/",
		"config": ["a.yaml", "b.yaml"],
		"secret_env": ["ENV=key"],
		"volumes": ["vol:/data"],
		"timeout": "5m",
		"reports": "reports",
		"policy": "policy.yaml",
		"evidence": "evidence",
		"dry_run": true,
		"yes": true,
		"extra_args": ["--foo"]
	}`
	out, err := DeployToolHandler(repoRoot)(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON: %v\n%s", err, out)
	}
	if result.Command != "deploy" {
		t.Fatalf("command = %q, want %q", result.Command, "deploy")
	}
	want := []string{
		"--name", "svc", "--namespace", "ns", "--replicas", "2", "--port", "8080",
		"--workload", "deployment", "--schedule", "0 * * * *",
		"--cpu-request", "100m", "--memory-request", "128Mi",
		"--runtime-class", "gvisor", "--ingress-host", "example.com", "--ingress-path", "/",
		"--config", "a.yaml", "--config", "b.yaml",
		"--secret-env", "ENV=key",
		"--volume", "vol:/data",
		"--timeout", "5m", "--reports", "reports", "--policy", "policy.yaml", "--evidence", "evidence",
		"--dry-run", "--yes", "--foo", "img@sha256:abc",
	}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

// TestDeployToolHandlerHonorsProjectRootOverride is F3's core assertion
// for pf_deploy: project_root, not the server's own repoRoot, becomes the
// subprocess's working directory when supplied.
func TestDeployToolHandlerHonorsProjectRootOverride(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	independentProject := t.TempDir()
	payload := fmt.Sprintf(`{"project_root":%q,"name":"svc"}`, independentProject)
	out, err := DeployToolHandler(repoRoot)(context.Background(), json.RawMessage(payload))
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

func TestDeployToolHandlerRejectsARelativeProjectRoot(t *testing.T) {
	_, err := DeployToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"project_root":"relative/path"}`))
	if err == nil {
		t.Fatal("expected an error for a relative project_root")
	}
}

func TestDeployToolHandlerOmitsZeroReplicasAndPort(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := DeployToolHandler(repoRoot)(context.Background(), json.RawMessage(`{"name":"svc"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	want := []string{"--name", "svc"}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

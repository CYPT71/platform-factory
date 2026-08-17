package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitAvailableForProvenanceTest(t *testing.T) bool {
	t.Helper()
	_, err := exec.LookPath("git")
	return err == nil
}

// provenanceTestModule creates a real git repository containing a go.mod
// and one commit - a real source tree, not a stub - plus a "built"
// executable file alongside it, mirroring what a plugin module's checkout
// plus its go-built binary actually look like.
func provenanceTestModule(t *testing.T) (sourceDir, executable string) {
	t.Helper()
	sourceDir = t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = sourceDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(sourceDir, "go.mod"), []byte("module github.com/CYPT71/platform-factory/plugins/example\n\ngo 1.25.12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "go.mod")
	run("commit", "-q", "-m", "initial")

	executable = filepath.Join(t.TempDir(), "example-plugin")
	if err := os.WriteFile(executable, []byte("fake plugin executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	return sourceDir, executable
}

func TestRunPluginProvenanceCapturesSourceBuilderArtifact(t *testing.T) {
	if !gitAvailableForProvenanceTest(t) {
		t.Skip("git not available")
	}
	sourceDir, executable := provenanceTestModule(t)

	var stdout, stderr bytes.Buffer
	code := runPluginProvenance([]string{
		"--executable", executable, "--name", "example", "--version", "1.0.0",
		"--source-dir", sourceDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}

	var record struct {
		BuildID    string `json:"build_id"`
		ArtifactID string `json:"artifact_id"`
		WorkerID   string `json:"worker_id"`
		Materials  []struct {
			Digest string `json:"digest"`
		} `json:"materials"`
		Invocation struct {
			ConfigSource struct {
				URI    string `json:"uri"`
				Digest string `json:"digest"`
			} `json:"config_source"`
		} `json:"invocation"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &record); err != nil {
		t.Fatalf("decode: %v\nstdout=%s", err, stdout.String())
	}
	if !strings.HasPrefix(record.ArtifactID, "sha256:") || len(record.ArtifactID) != 71 {
		t.Fatalf("ArtifactID=%q", record.ArtifactID)
	}
	if record.BuildID != "example@"+record.ArtifactID {
		t.Fatalf("BuildID=%q", record.BuildID)
	}
	if record.WorkerID != "https://platform-factory.dev/builder/v1" {
		t.Fatalf("WorkerID=%q", record.WorkerID)
	}
	if len(record.Materials) != 1 || len(record.Materials[0].Digest) != 40 {
		t.Fatalf("Materials=%+v", record.Materials)
	}
	if record.Invocation.ConfigSource.URI != "github.com/CYPT71/platform-factory/plugins/example" {
		t.Fatalf("ConfigSource.URI=%q, want the module path read from go.mod", record.Invocation.ConfigSource.URI)
	}
	if record.Signature != "" {
		t.Fatalf("record signed without --sign: %+v", record)
	}
}

func TestRunPluginProvenanceSignsWithKeyDir(t *testing.T) {
	if !gitAvailableForProvenanceTest(t) {
		t.Skip("git not available")
	}
	sourceDir, executable := provenanceTestModule(t)
	keyDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := runPluginProvenance([]string{
		"--executable", executable, "--name", "example",
		"--source-dir", sourceDir, "--sign", "--key-dir", keyDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"signature":`) || strings.Contains(stdout.String(), `"signature": ""`) {
		t.Fatalf("record was not signed: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"signed_by": "plugin-provenance"`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestRunPluginProvenanceRejectsMissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runPluginProvenance(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestRunPluginProvenanceFailsClosedWithoutGitHistory(t *testing.T) {
	if !gitAvailableForProvenanceTest(t) {
		t.Skip("git not available")
	}
	executable := filepath.Join(t.TempDir(), "plugin")
	if err := os.WriteFile(executable, []byte("bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runPluginProvenance([]string{
		"--executable", executable, "--name", "example", "--source-dir", t.TempDir(),
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("succeeded with no git history: stdout=%s", stdout.String())
	}
}

func TestRunPluginProvenanceWritesToOutFile(t *testing.T) {
	if !gitAvailableForProvenanceTest(t) {
		t.Skip("git not available")
	}
	sourceDir, executable := provenanceTestModule(t)
	outFile := filepath.Join(t.TempDir(), "provenance.json")

	var stdout, stderr bytes.Buffer
	code := runPluginProvenance([]string{
		"--executable", executable, "--name", "example",
		"--source-dir", sourceDir, "--out", outFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout should be empty when --out is used, got %s", stdout.String())
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"artifact_id"`) {
		t.Fatalf("out file content=%s", data)
	}
}

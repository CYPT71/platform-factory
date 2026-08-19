package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
)

func TestProjectLogsAndEventsUsePersistedDeploymentIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteJSONSensitive(filepath.Join(root, ".platform-factory", "deployed.json"), map[string]any{
		"api_version": "platform-factory.dev/deployment/v1",
		"image":       "registry.example/app@sha256:" + strings.Repeat("a", 64),
		"name":        "hello", "namespace": "prod", "workload": "job",
	}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var calls [][]string
	execute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runProjectObservation("logs", []string{"--tail", "50", "--follow"}, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("logs code=%d stderr=%s", code, stderr.String())
	}
	if code := runProjectObservation("events", nil, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("events code=%d stderr=%s", code, stderr.String())
	}
	want := [][]string{
		{"kubectl", "logs", "job/hello", "--namespace", "prod", "--tail", "50", "--follow"},
		{"kubectl", "get", "events", "--namespace", "prod", "--field-selector", "involvedObject.name=hello", "--sort-by=.lastTimestamp"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestProjectLogsFailWithOneSafeNextAction(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := runProjectObservation("logs", nil, &stdout, &stderr, nil); code != 1 ||
		!strings.Contains(stderr.String(), "run `pf deploy` first") {
		t.Fatalf("code/status=%s", stderr.String())
	}
}

func TestRollbackUsesPersistedServiceAndRejectsJob(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, ".platform-factory", "deployed.json")
	state := map[string]any{
		"api_version": "platform-factory.dev/deployment/v1",
		"image":       "registry.example/app@sha256:" + strings.Repeat("a", 64),
		"name":        "hello", "namespace": "prod", "workload": "service",
	}
	if err := atomicfile.WriteJSONSensitive(statePath, state); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := runRollback([]string{"--dry-run"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("rollback code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "deployment/hello") || !strings.Contains(stdout.String(), "--namespace prod") {
		t.Fatalf("rollback plan=%s", stdout.String())
	}
	state["workload"] = "job"
	if err := atomicfile.WriteJSONSensitive(statePath, state); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runRollback([]string{"--dry-run"}, &stdout, &stderr, nil); code != 1 ||
		!strings.Contains(stderr.String(), "Jobs have no rollout history") {
		t.Fatalf("job rollback code=%d stderr=%s", code, stderr.String())
	}
}

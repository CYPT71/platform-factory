package observe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProject(t *testing.T, root string, deployed map[string]any) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if deployed == nil {
		return
	}
	encoded, err := json.Marshal(deployed)
	if err != nil {
		t.Fatal(err)
	}
	deployDir := filepath.Join(root, ".platform-factory")
	if err := os.MkdirAll(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deployDir, "deployed.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDeployedProjectValid(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, map[string]any{
		"api_version": "platform-factory.dev/deployment/v1",
		"image":       "registry.example/app@sha256:" + strings.Repeat("a", 64),
		"name":        "hello", "namespace": "prod", "workload": "job",
	})
	t.Chdir(root)
	state, err := LoadDeployedProject()
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if state.Name != "hello" || state.Namespace != "prod" || state.Workload != "job" {
		t.Fatalf("state=%+v", state)
	}
}

func TestLoadDeployedProjectMissingFile(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, nil)
	t.Chdir(root)
	if _, err := LoadDeployedProject(); err == nil {
		t.Fatal("expected error for missing deployed.json")
	}
}

func TestLoadDeployedProjectRejectsInvalidIdentity(t *testing.T) {
	root := t.TempDir()
	writeProject(t, root, map[string]any{
		"api_version": "platform-factory.dev/deployment/v1",
		"image":       "not-a-digest-reference",
		"name":        "hello", "namespace": "prod", "workload": "job",
	})
	t.Chdir(root)
	if _, err := LoadDeployedProject(); err == nil {
		t.Fatal("expected error for invalid digest reference")
	}
}

func TestLoadDeployReceiptSupportsEveryRenderedWorkloadAndRuntimeClass(t *testing.T) {
	for _, workload := range []string{"service", "job", "statefulset", "daemonset", "cronjob"} {
		t.Run(workload, func(t *testing.T) {
			root := t.TempDir()
			writeProject(t, root, map[string]any{"api_version": "platform-factory.dev/deployment/v1", "image": "registry.example/app@sha256:" + strings.Repeat("b", 64), "name": "hello", "namespace": "acceptance", "workload": workload, "runtime_class": "platform-factory"})
			t.Chdir(root)
			state, err := LoadDeployedProject()
			if err != nil || state.Workload != workload || state.RuntimeClass != "platform-factory" {
				t.Fatalf("state=%+v err=%v", state, err)
			}
		})
	}
}

func TestLoadDeployReceiptRejectsInvalidRuntimeClassAndUnknownFields(t *testing.T) {
	for _, invalid := range []string{"runtime_class", "unknown_field", "workload"} {
		t.Run(invalid, func(t *testing.T) {
			receipt := map[string]any{"api_version": "platform-factory.dev/deployment/v1", "image": "registry.example/app@sha256:" + strings.Repeat("c", 64), "name": "hello", "namespace": "acceptance", "workload": "service", "runtime_class": ""}
			receipt[invalid] = "invalid/value"
			root := t.TempDir()
			writeProject(t, root, receipt)
			t.Chdir(root)
			if _, err := LoadDeployedProject(); err == nil {
				t.Fatal("accepted invalid receipt", invalid)
			}
		})
	}
}

package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/app/projectinit"
	"github.com/CYPT71/secure-oci-base/internal/detect"
)

func TestResolveEcosystemInteractivelyLeavesConfidentResultAlone(t *testing.T) {
	result := detect.Result{Kind: "go", Evidence: []string{"go.mod"}}
	stdin := bufio.NewReader(strings.NewReader("should never be read\n"))
	var stdout bytes.Buffer
	eco := resolveEcosystemInteractively(result, "", "", false, stdin, &stdout)
	if eco.result.Kind != "go" || eco.artifact != "" || !eco.confident {
		t.Fatalf("eco=%+v", eco)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no prompt output, got %q", stdout.String())
	}
}

func TestResolveEcosystemInteractivelySkipsPromptWhenAssumeYes(t *testing.T) {
	result := detect.Result{Ambiguous: true, Candidates: []string{"go", "node"}}
	stdin := bufio.NewReader(strings.NewReader("go\n"))
	var stdout bytes.Buffer
	eco := resolveEcosystemInteractively(result, "", "", true, stdin, &stdout)
	if !eco.result.Ambiguous || eco.artifact != "" || eco.confident {
		t.Fatalf("eco=%+v, want unchanged and not confident (assumeYes)", eco)
	}
}

func TestResolveEcosystemInteractivelySkipsPromptWhenStdinNil(t *testing.T) {
	result := detect.Result{Kind: "unknown"}
	var stdout bytes.Buffer
	eco := resolveEcosystemInteractively(result, "", "", false, nil, &stdout)
	if eco.result.Kind != "unknown" || eco.artifact != "" || eco.confident {
		t.Fatalf("eco=%+v, want unchanged and not confident (nil stdin)", eco)
	}
}

func TestResolveEcosystemInteractivelyReadsLanguageAndArtifact(t *testing.T) {
	result := detect.Result{Ambiguous: true, Candidates: []string{"go", "node"}}
	stdin := bufio.NewReader(strings.NewReader("1\ncmd/service/main.go\n")) // 1 = Go in the numbered menu
	var stdout bytes.Buffer
	eco := resolveEcosystemInteractively(result, "", "", false, stdin, &stdout)
	if eco.result.Kind != "go" || eco.result.Ambiguous || !eco.confident {
		t.Fatalf("eco.result=%+v confident=%v", eco.result, eco.confident)
	}
	if eco.artifact != "cmd/service/main.go" {
		t.Fatalf("artifact=%q", eco.artifact)
	}
	if !strings.Contains(stdout.String(), "could be more than one kind of project: go, node") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "1) Go") || !strings.Contains(stdout.String(), "9) Custom") {
		t.Fatalf("expected the full numbered menu, stdout=%s", stdout.String())
	}
}

func TestResolveEcosystemInteractivelyRejectsOutOfRangeChoice(t *testing.T) {
	result := detect.Result{Kind: "unknown"}
	stdin := bufio.NewReader(strings.NewReader("0\n"))
	var stdout bytes.Buffer
	eco := resolveEcosystemInteractively(result, "", "", false, stdin, &stdout)
	if eco.confident {
		t.Fatalf("eco=%+v, want not confident for an out-of-range menu choice", eco)
	}
	if !strings.Contains(stdout.String(), `"0" isn't one of the numbers above`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestResolveEcosystemInteractivelyEmptyLanguageIsNotConfident(t *testing.T) {
	result := detect.Result{Kind: "unknown"}
	stdin := bufio.NewReader(strings.NewReader("\n"))
	var stdout bytes.Buffer
	eco := resolveEcosystemInteractively(result, "", "", false, stdin, &stdout)
	if eco.result.Kind != "unknown" || eco.artifact != "" || eco.confident {
		t.Fatalf("eco=%+v, want not confident (empty answer)", eco)
	}
}

func TestResolveEcosystemInteractivelyLanguageFlagWinsWithoutPrompting(t *testing.T) {
	result := detect.Result{Kind: "unknown"}
	stdin := bufio.NewReader(strings.NewReader("should never be read\n"))
	var stdout bytes.Buffer
	eco := resolveEcosystemInteractively(result, "custom", "bin/app", false, stdin, &stdout)
	if !eco.confident || eco.result.Kind != "custom" || eco.artifact != "bin/app" {
		t.Fatalf("eco=%+v", eco)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no prompt output when --language is given, got %q", stdout.String())
	}
}

func TestResolveEcosystemInteractivelyArtifactFlagSkipsThatPromptOnly(t *testing.T) {
	result := detect.Result{Kind: "unknown"}
	stdin := bufio.NewReader(strings.NewReader("1\n")) // only the language answer is needed (1 = Go)
	var stdout bytes.Buffer
	eco := resolveEcosystemInteractively(result, "", "bin/app", false, stdin, &stdout)
	if !eco.confident || eco.result.Kind != "go" || eco.artifact != "bin/app" {
		t.Fatalf("eco=%+v", eco)
	}
	if strings.Contains(stdout.String(), "where's the file your app starts from") {
		t.Fatalf("should not prompt for artifact when --artifact is given; stdout=%s", stdout.String())
	}
}

func TestConfirmPlanBypassesPromptWhenAssumeYesOrStdinNil(t *testing.T) {
	var stdout bytes.Buffer
	if !confirmPlan(projectinit.Plan{}, "/tmp/x", true, bufio.NewReader(strings.NewReader("n\n")), &stdout) {
		t.Fatal("assumeYes should bypass the prompt and proceed")
	}
	if !confirmPlan(projectinit.Plan{}, "/tmp/x", false, nil, &stdout) {
		t.Fatal("nil stdin should bypass the prompt and proceed")
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no prompt output, got %q", stdout.String())
	}
}

func TestConfirmPlanRespectsYesOrNoAnswer(t *testing.T) {
	dir := t.TempDir()
	component := filepath.Join(dir, "api")
	if err := os.Mkdir(component, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(component, "go.mod"), []byte("module example/api\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := projectinit.BuildPlan(dir, projectinit.Ecosystem{Result: detect.Result{Kind: "go"}, Artifact: "app", Confident: true}, nil, projectinit.Observations{GeneratedAt: time.Unix(0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if !confirmPlan(plan, "/tmp/x", false, bufio.NewReader(strings.NewReader("y\n")), &stdout) {
		t.Fatal("'y' should proceed")
	}
	stdout.Reset()
	if confirmPlan(plan, "/tmp/x", false, bufio.NewReader(strings.NewReader("n\n")), &stdout) {
		t.Fatal("'n' should abort")
	}
	stdout.Reset()
	if confirmPlan(plan, "/tmp/x", false, bufio.NewReader(strings.NewReader("\n")), &stdout) {
		t.Fatal("an empty answer should default to abort, not proceed")
	}
	if !strings.Contains(stdout.String(), "platform-factory.yaml") || !strings.Contains(stdout.String(), "component api from api: recommended runtime container") || !strings.Contains(stdout.String(), "unknown resources") {
		t.Fatalf("expected the plan to be printed, got %q", stdout.String())
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/microvm"
	"github.com/CYPT71/platform-factory/internal/observability"
	"github.com/CYPT71/platform-factory/internal/policy"
)

func TestBuildRejectsInvalidMachineInputs(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "app")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "missing")
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"flag", []string{"--unknown"}, "flag provided"},
		{"created", []string{"--created", "tomorrow", binary}, "invalid --created"},
		{"format", []string{"--format", "xml", binary}, "format must be"},
		{"label", []string{"--label", "invalid", binary}, "label"},
		{"config", []string{"--config", missing, binary}, "missing"},
		{"extra", []string{"--extra-file", "invalid", binary}, "extra file"},
		{"dry-run-input", []string{"--dry-run", missing}, "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runBuild(commandContext(context.Background(), "build"), test.args, &stdout, &stderr); code == 0 {
				t.Fatalf("invalid input accepted: stdout=%s", stdout.String())
			}
			if !strings.Contains(strings.ToLower(stderr.String()), strings.ToLower(test.want)) {
				t.Fatalf("stderr=%q want=%q", stderr.String(), test.want)
			}
		})
	}
}

func TestReleaseTextRendersFailedAndSkippedEvidence(t *testing.T) {
	result := releaseVerification{
		Digest: "sha256:test", SignatureError: "bad signature", ProvenanceValid: true,
		PolicyError: "policy unavailable", Valid: false,
	}
	var output bytes.Buffer
	printReleaseVerificationText(&output, result)
	for _, want := range []string{"signature\tFAIL: bad signature", "provenance\tok", "sbom\tskipped", "policy\tFAIL: policy unavailable", "release\tvalid=false"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q: %s", want, output.String())
		}
	}
	output.Reset()
	result.PolicyError = ""
	result.PolicyDecision = &policy.Decision{Allowed: false, Reasons: []string{"denied"}}
	printReleaseVerificationText(&output, result)
	if !strings.Contains(output.String(), "policy\tallowed=false\tdenied") {
		t.Fatalf("output=%s", output.String())
	}
	output.Reset()
	result.PolicyDecision = nil
	printReleaseVerificationText(&output, result)
	if !strings.Contains(output.String(), "policy\tskipped") {
		t.Fatalf("output=%s", output.String())
	}
}

func TestNativeLifecycleFailuresRemainExplicit(t *testing.T) {
	execute := func(string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		return errors.New("service manager unavailable")
	}
	for _, action := range []string{"stop", "restart", "status", "logs", "delete", "unsupported"} {
		var stdout, stderr bytes.Buffer
		code := runNative(action, microvm.Spec{Name: "demo"}, "runner", false, &stdout, &stderr, execute)
		if code == 0 {
			t.Fatalf("action %s succeeded", action)
		}
	}
}

func TestNativeEligibilityRejectsUnsupportedCPUCountBeforeProbe(t *testing.T) {
	ok, reason := nativeKVMEligible(context.Background(), microvm.Spec{VCPUs: 2})
	if ok || !strings.Contains(reason, "exactly one vCPU") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
}

func TestLaunchJSONRejectsUnserializableEvidence(t *testing.T) {
	if err := atomicfile.WriteJSONSensitive(filepath.Join(t.TempDir(), "evidence.json"), make(chan int)); err == nil {
		t.Fatal("unserializable evidence accepted")
	}
}

func TestBuildPropagatesCallerTraceToMachineEvidence(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "app")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	traceID := "cli-contract-trace"
	ctx := observability.ContextWithTraceID(context.Background(), traceID)
	var stdout, stderr bytes.Buffer
	if code := runBuild(commandContext(ctx, "build"), []string{"--output", filepath.Join(root, "layout"), binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `"trace_id":"`+traceID+`"`) {
		t.Fatalf("caller trace not propagated: %s", stderr.String())
	}
}

func TestCommandContextHonorsExternalTraceIDEnvVar(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_TRACE_ID", "external-correlation-id")
	ctx := commandContext(context.Background(), "build")
	if got := observability.TraceIDFromContext(ctx); got != "external-correlation-id" {
		t.Fatalf("trace_id=%q, want the env var's value", got)
	}
}

func TestCommandContextGeneratesATraceIDWhenNoneIsSupplied(t *testing.T) {
	ctx := commandContext(context.Background(), "build")
	if got := observability.TraceIDFromContext(ctx); got == "" {
		t.Fatal("commandContext produced no trace ID")
	}
}

func TestGlobalQuietSuppressesSuccessfulOutputAnywhereInArguments(t *testing.T) {
	for _, args := range [][]string{{"--quiet", "version"}, {"version", "-q"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
		if stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("args=%v stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
}

func TestGlobalVerboseReportsCommandAndTraceWithoutEchoingArguments(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_TRACE_ID", "demo-trace")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version", "--verbose"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "command=version trace_id=demo-trace") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestGlobalQuietAndVerboseAreMutuallyExclusive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--quiet", "version", "--verbose"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestReadOnlyCommandsRejectMalformedRequests(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
		want string
	}{
		{"inspect-help", func(o, e *bytes.Buffer) int { return runInspect("inspect", []string{"--help"}, o, e) }, ""},
		{"inspect-format", func(o, e *bytes.Buffer) int {
			return runInspect("inspect", []string{"--format", "xml", "layout"}, o, e)
		}, "format"},
		{"inspect-args", func(o, e *bytes.Buffer) int { return runInspect("verify", nil, o, e) }, "usage"},
		{"inspect-missing", func(o, e *bytes.Buffer) int { return runInspect("inspect", []string{"missing"}, o, e) }, "inspect"},
		{"compose-flag", func(o, e *bytes.Buffer) int { return runCompose([]string{"--unknown"}, o, e) }, "flag"},
		{"compose-args", func(o, e *bytes.Buffer) int { return runCompose([]string{"one"}, o, e) }, "usage"},
		{"compose-format", func(o, e *bytes.Buffer) int { return runCompose([]string{"--format", "xml", "one", "two"}, o, e) }, "format"},
		{"compose-invalid-layout", func(o, e *bytes.Buffer) int { return runCompose([]string{"missing-a", "missing-b"}, o, e) }, "compose"},
		{"import-help", func(o, e *bytes.Buffer) int { return runImport(context.Background(), []string{"--help"}, o, e) }, ""},
		{"import-args", func(o, e *bytes.Buffer) int { return runImport(context.Background(), nil, o, e) }, "usage"},
		{"import-runtime", func(o, e *bytes.Buffer) int { return runImport(context.Background(), []string{"--runtime", "bad", "image"}, o, e) }, "usage"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := test.run(&stdout, &stderr)
			if test.name == "inspect-help" || test.name == "import-help" {
				if code != 0 {
					t.Fatalf("help code=%d stderr=%s", code, stderr.String())
				}
				return
			}
			if code == 0 || !strings.Contains(strings.ToLower(stderr.String()), test.want) {
				t.Fatalf("code=%d stderr=%q want=%q", code, stderr.String(), test.want)
			}
		})
	}
}

func TestContainerRuntimeFailureIsMachineClassified(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runContainer(context.Background(), []string{"service:local"}, &stdout, &stderr,
		func(string, []string, io.Reader, io.Writer, io.Writer) error {
			return errors.New("runtime unavailable")
		})
	if code != 1 || !strings.Contains(stderr.String(), "runtime unavailable") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

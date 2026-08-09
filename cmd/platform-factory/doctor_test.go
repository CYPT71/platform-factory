package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/app/doctor"
)

// These tests exercise runDoctor's CLI facade behavior only - argument
// parsing, output formatting, exit codes. The diagnostics themselves
// (what counts as OK, what suggestion a failure gets) are tested against
// injected fakes in internal/app/doctor, not here - see that package's
// own tests for TestCheckToolReportsAbsentBinaryWithSuggestion and
// equivalents.

func TestRunDoctorTextReportListsEveryCheck(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runDoctor(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{
		"tool-git", "tool-docker", "tool-podman", "tool-containerd", "tool-kubectl",
		"native-hypervisor", "sandbox-namespaces", "sandbox-cgroups", "sandbox-capability-bounding-drop",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("report missing %q: %s", want, stdout.String())
		}
	}
}

func TestRunDoctorJSONReportIsWellFormedAndConsistent(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runDoctor([]string{"--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var report doctor.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid doctor JSON: %v: %s", err, stdout.String())
	}
	if len(report.Checks) < 14 {
		t.Fatalf("checks=%+v, want at least 14", report.Checks)
	}
	allOK := true
	for _, c := range report.Checks {
		if c.Name == "" {
			t.Fatalf("check with empty name: %+v", report.Checks)
		}
		if !c.OK {
			allOK = false
			if c.Suggestion == "" && c.Name != "native-hypervisor" {
				t.Fatalf("failing check %q has no suggestion", c.Name)
			}
		}
	}
	if report.OK != allOK {
		t.Fatalf("report.OK=%v, want %v (derived from individual checks)", report.OK, allOK)
	}
}

func TestRunDoctorIncludesRuntimeRegistryAndKubernetesChecks(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runDoctor(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"runtime-docker", "runtime-podman", "runtime-containerd", "runtime-kubernetes", "registry-configured"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("report missing %q: %s", want, stdout.String())
		}
	}
}

func TestRunDoctorRejectsUnknownFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runDoctor([]string{"--nope"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

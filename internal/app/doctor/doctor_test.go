package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/hypervisor/sandbox"
	"github.com/CYPT71/secure-oci-base/internal/microvm"
)

// fakeService builds a Service with every dependency faked, so tests
// never shell out to a real tool or probe real hardware.
func fakeService() (svc Service, lookPathCalls, runCommandCalls *[]string) {
	var lookPaths, runs []string
	svc = Service{
		LookPath: func(name string) (string, error) {
			lookPaths = append(lookPaths, name)
			return "", errors.New("not found") // absent by default; tests override per case
		},
		RunCommand: func(ctx context.Context, name string, args ...string) error {
			runs = append(runs, name)
			return errors.New("should not be called for an absent tool")
		},
		FileExists:   func(path string) bool { return false },
		UserHomeDir:  func() (string, error) { return "/home/test", nil },
		ProbeNative:  func(context.Context) (microvm.Capabilities, error) { return microvm.Capabilities{}, nil },
		ProbeSandbox: func() sandbox.Support { return sandbox.Support{} },
	}
	return svc, &lookPaths, &runs
}

func TestRunReportsToolMissingWithoutAttemptingARuntimeCheck(t *testing.T) {
	svc, _, runs := fakeService()
	report := svc.Run(context.Background())

	found := false
	for _, c := range report.Checks {
		if c.Name == "runtime-docker" {
			found = true
			if c.OK {
				t.Fatal("runtime-docker should not be OK when tool-docker was never found")
			}
			if c.Suggestion == "" {
				t.Fatal("expected a suggestion when the tool itself is missing")
			}
		}
	}
	if !found {
		t.Fatal("expected a runtime-docker check in the report")
	}
	if len(*runs) != 0 {
		t.Fatalf("RunCommand should never be called for a tool LookPath didn't find, got calls=%v", *runs)
	}
}

func TestRunOnlyReportsRuntimeOKWhenTheCommandActuallySucceeds(t *testing.T) {
	svc, _, _ := fakeService()
	svc.LookPath = func(name string) (string, error) {
		if name == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", errors.New("not found")
	}
	svc.RunCommand = func(ctx context.Context, name string, args ...string) error {
		if name == "docker" {
			return nil // daemon reachable
		}
		return errors.New("unexpected")
	}
	report := svc.Run(context.Background())

	var runtimeDocker, toolDocker *Check
	for i := range report.Checks {
		switch report.Checks[i].Name {
		case "runtime-docker":
			runtimeDocker = &report.Checks[i]
		case "tool-docker":
			toolDocker = &report.Checks[i]
		}
	}
	if toolDocker == nil || !toolDocker.OK {
		t.Fatalf("tool-docker=%+v", toolDocker)
	}
	if runtimeDocker == nil || !runtimeDocker.OK {
		t.Fatalf("runtime-docker should be OK when RunCommand succeeded, got %+v", runtimeDocker)
	}
}

func TestRunReportsRuntimeNotOKWhenTheCommandFailsEvenIfToolIsInstalled(t *testing.T) {
	svc, _, _ := fakeService()
	svc.LookPath = func(name string) (string, error) {
		if name == "podman" {
			return "/usr/bin/podman", nil
		}
		return "", errors.New("not found")
	}
	svc.RunCommand = func(ctx context.Context, name string, args ...string) error {
		return errors.New("podman machine is not running")
	}
	report := svc.Run(context.Background())

	for _, c := range report.Checks {
		if c.Name == "runtime-podman" {
			if c.OK {
				t.Fatal("runtime-podman should not be OK when the probe command failed")
			}
			if c.Detail == "" {
				t.Fatal("expected the command failure to be surfaced in Detail")
			}
			return
		}
	}
	t.Fatal("expected a runtime-podman check")
}

func TestRegistryConfiguredChecksRealFilesNotAPlaceholder(t *testing.T) {
	svc, _, _ := fakeService()
	svc.FileExists = func(path string) bool { return false }
	report := svc.Run(context.Background())
	assertCheck(t, report, "registry-configured", false)

	svc.FileExists = func(path string) bool { return true }
	report = svc.Run(context.Background())
	assertCheck(t, report, "registry-configured", true)
}

func TestRunReportsOverallOKOnlyWhenEveryCheckPasses(t *testing.T) {
	svc, _, _ := fakeService()
	svc.ProbeNative = func(context.Context) (microvm.Capabilities, error) {
		return microvm.Capabilities{Available: true, Architecture: "arm64"}, nil
	}
	svc.ProbeSandbox = func() sandbox.Support {
		return sandbox.Support{Namespaces: true, Cgroups: true, CapabilityBoundingDrop: true}
	}
	report := svc.Run(context.Background())
	// tool-*/runtime-*/registry-configured are still failing (fakeService
	// defaults everything absent), so the aggregate must be false even
	// though hypervisor/sandbox are fully healthy.
	if report.OK {
		t.Fatal("report.OK should be false while any check is failing")
	}
}

func TestRunAggregatesOKTrueWhenEverythingPasses(t *testing.T) {
	svc, _, _ := fakeService()
	svc.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	svc.RunCommand = func(context.Context, string, ...string) error { return nil }
	svc.FileExists = func(string) bool { return true }
	svc.ProbeNative = func(context.Context) (microvm.Capabilities, error) {
		return microvm.Capabilities{Available: true}, nil
	}
	svc.ProbeSandbox = func() sandbox.Support {
		return sandbox.Support{Namespaces: true, Cgroups: true, CapabilityBoundingDrop: true}
	}
	report := svc.Run(context.Background())
	if !report.OK {
		for _, c := range report.Checks {
			if !c.OK {
				t.Logf("failing check: %+v", c)
			}
		}
		t.Fatal("expected report.OK=true when every individual check passes")
	}
}

func assertCheck(t *testing.T, report Report, name string, wantOK bool) {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == name {
			if c.OK != wantOK {
				t.Fatalf("%s.OK=%v, want %v (check=%+v)", name, c.OK, wantOK, c)
			}
			return
		}
	}
	t.Fatalf("no check named %q in report", name)
}

func TestNewWiresRealDependenciesWithoutPanicking(t *testing.T) {
	// Smoke test only: New() must construct a usable Service against
	// the real toolchain without nil-dereferencing, whether or not any
	// of these tools happen to be installed on the machine running the
	// test.
	svc := New()
	report := svc.Run(context.Background())
	if report.Checks == nil {
		t.Fatal("expected at least one check")
	}
}

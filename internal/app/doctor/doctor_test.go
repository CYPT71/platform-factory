package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/CYPT71/platform-factory/internal/hypervisor/sandbox"
	"github.com/CYPT71/platform-factory/internal/microvm"
)

// fakeService builds a Service with every dependency faked, so tests
// never shell out to a real tool or probe real hardware.
func fakeService() (svc *service, lookPathCalls, runCommandCalls *[]string) {
	var lookPaths, runs []string
	svc = &service{
		lookPath: func(name string) (string, error) {
			lookPaths = append(lookPaths, name)
			return "", errors.New("not found") // absent by default; tests override per case
		},
		runCommand: func(ctx context.Context, name string, args ...string) error {
			runs = append(runs, name)
			return errors.New("should not be called for an absent tool")
		},
		fileExists:    func(path string) bool { return false },
		userHomeDir:   func() (string, error) { return "/home/test", nil },
		probeNative:   func(context.Context) (microvm.Capabilities, error) { return microvm.Capabilities{}, nil },
		probeSandbox:  func() sandbox.Support { return sandbox.Support{} },
		probeRegistry: func(context.Context, string) error { return errors.New("unreachable") },
		readFile:      func(string) ([]byte, error) { return nil, errors.New("missing") },
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
	svc.lookPath = func(name string) (string, error) {
		if name == "docker" {
			return "/usr/bin/docker", nil
		}
		return "", errors.New("not found")
	}
	svc.runCommand = func(ctx context.Context, name string, args ...string) error {
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
	svc.lookPath = func(name string) (string, error) {
		if name == "podman" {
			return "/usr/bin/podman", nil
		}
		return "", errors.New("not found")
	}
	svc.runCommand = func(ctx context.Context, name string, args ...string) error {
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
	svc.fileExists = func(path string) bool { return false }
	report := svc.Run(context.Background())
	assertCheck(t, report, "registry-configured", false)

	svc.fileExists = func(path string) bool { return true }
	report = svc.Run(context.Background())
	assertCheck(t, report, "registry-configured", true)
}

func TestRunReportsOverallOKOnlyWhenEveryCheckPasses(t *testing.T) {
	svc, _, _ := fakeService()
	svc.probeNative = func(context.Context) (microvm.Capabilities, error) {
		return microvm.Capabilities{Available: true, Architecture: "arm64"}, nil
	}
	svc.probeSandbox = func() sandbox.Support {
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
	svc.lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	svc.runCommand = func(context.Context, string, ...string) error { return nil }
	svc.fileExists = func(string) bool { return true }
	svc.probeNative = func(context.Context) (microvm.Capabilities, error) {
		return microvm.Capabilities{Available: true}, nil
	}
	svc.probeSandbox = func() sandbox.Support {
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

func TestRunScopeDeployChecksOnlyKubectl(t *testing.T) {
	svc, lookups, _ := fakeService()
	report := svc.RunScope(context.Background(), "deploy")
	if len(*lookups) != 1 || (*lookups)[0] != "kubectl" {
		t.Fatalf("lookups=%v", *lookups)
	}
	for _, check := range report.Checks {
		if check.Name != "tool-kubectl" && check.Name != "runtime-kubernetes" {
			t.Fatalf("deploy scope leaked unrelated check %+v", check)
		}
	}
}

func TestRunScopePublishDoesNotProbeUnrelatedToolsOrHardware(t *testing.T) {
	svc, lookups, runs := fakeService()
	report := svc.RunScope(context.Background(), "publish")
	if len(*lookups) != 0 || len(*runs) != 0 {
		t.Fatalf("lookups=%v runs=%v", *lookups, *runs)
	}
	if len(report.Checks) != 1 || report.Checks[0].Name != "registry-configured" {
		t.Fatalf("checks=%+v", report.Checks)
	}
}

func TestContainerdRuntimeUsesCtrClient(t *testing.T) {
	svc, _, _ := fakeService()
	var invoked string
	svc.lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	svc.runCommand = func(_ context.Context, name string, args ...string) error {
		if len(args) == 1 && args[0] == "version" {
			invoked = name
		}
		return nil
	}
	svc.fileExists = func(string) bool { return true }
	svc.probeNative = nil
	svc.probeSandbox = nil
	report := svc.Run(context.Background())
	assertCheck(t, report, "runtime-containerd", true)
	if invoked != "ctr" {
		t.Fatalf("runtime-containerd invoked %q, want ctr", invoked)
	}
}

func TestHypervisorChecksReportCurrentBackendAndSkipOtherPlatforms(t *testing.T) {
	checks := hypervisorChecks(microvm.Capabilities{
		Available: true, Architecture: "amd64",
		Details: map[string]string{"backend": "linux-kvm-native"},
	}, nil)
	byName := map[string]Check{}
	for _, check := range checks {
		byName[check.Name] = check
	}
	if !byName["kvm-device"].OK || !byName["kvm-extensions"].OK {
		t.Fatalf("KVM checks=%+v", checks)
	}
	for _, name := range []string{"hyper-v", "virtualization-framework"} {
		if !byName[name].Skipped || byName[name].OK {
			t.Fatalf("%s=%+v, want skipped", name, byName[name])
		}
	}
}

func TestSkippedPlatformChecksDoNotFailAggregate(t *testing.T) {
	svc, _, _ := fakeService()
	svc.lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	svc.runCommand = func(context.Context, string, ...string) error { return nil }
	svc.fileExists = func(string) bool { return true }
	svc.probeNative = func(context.Context) (microvm.Capabilities, error) {
		return microvm.Capabilities{Available: true, Details: map[string]string{"backend": "darwin-native-virtualization"}}, nil
	}
	svc.probeSandbox = func() sandbox.Support {
		return sandbox.Support{Namespaces: true, Cgroups: true, CapabilityBoundingDrop: true}
	}
	if report := svc.Run(context.Background()); !report.OK {
		t.Fatalf("report=%+v", report)
	}
}

func TestRunScopeWithOptionsProbesNamedRegistry(t *testing.T) {
	svc, _, _ := fakeService()
	var address string
	svc.probeRegistry = func(_ context.Context, value string) error { address = value; return nil }
	report := svc.RunScopeWithOptions(context.Background(), "publish", Options{Registry: "registry.example.com"})
	assertCheck(t, report, "registry-access", true)
	if address != "registry.example.com" {
		t.Fatalf("address=%q", address)
	}
	for _, check := range report.Checks {
		if check.Name == "registry-configured" {
			t.Fatalf("configuration is not an access proof: %+v", report.Checks)
		}
	}
}

func TestPolicyCheckStrictlyValidatesSchemaAndVersion(t *testing.T) {
	svc, _, _ := fakeService()
	svc.readFile = func(string) ([]byte, error) {
		return []byte(`{"api_version":"platform-factory.dev/policy/v1","require_sbom":true}`), nil
	}
	assertCheck(t, svc.RunScopeWithOptions(context.Background(), "build", Options{Policy: "policy.json"}), "policy", true)
	svc.readFile = func(string) ([]byte, error) {
		return []byte(`{"api_version":"platform-factory.dev/policy/v1","surprise":true}`), nil
	}
	assertCheck(t, svc.RunScopeWithOptions(context.Background(), "build", Options{Policy: "policy.json"}), "policy", false)
	svc.readFile = func(string) ([]byte, error) {
		return []byte(`{"api_version":"unsupported/v1"}`), nil
	}
	assertCheck(t, svc.RunScopeWithOptions(context.Background(), "build", Options{Policy: "policy.json"}), "policy", false)
}

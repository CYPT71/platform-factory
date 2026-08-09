package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/executor"
	"github.com/CYPT71/secure-oci-base/internal/hypervisor"
	"github.com/CYPT71/secure-oci-base/internal/layout"
	"github.com/CYPT71/secure-oci-base/internal/microvm"
	"github.com/CYPT71/secure-oci-base/internal/networking"
	"github.com/CYPT71/secure-oci-base/internal/oci"
	"github.com/CYPT71/secure-oci-base/internal/plugin"
)

// nativeKVMAvailableForTest reports, for real, whether this test process's
// own host can actually run the native KVM microVM backend - the same
// probe nativeKVMEligible itself calls. Tests that care whether run/start
// dispatch to the native backend or fall back to run-microvm.sh/QEMU use
// this to assert the behavior real for wherever they happen to run, rather
// than injecting a fake probe result.
func nativeKVMAvailableForTest(t *testing.T) bool {
	t.Helper()
	capabilities, err := hypervisor.ProbeNative(context.Background())
	if err != nil {
		t.Fatalf("hypervisor.ProbeNative: %v", err)
	}
	return capabilities.Available
}

// TestMain mirrors exactly what main() calls, and for the same reason
// internal/executor's own tests do this (see its TestMain): the sandboxed
// executor, memory-limited executor and sandboxed plugin subprocess all
// re-exec the current binary as their helper. When tests run through this
// compiled test binary rather than a separately built platform-factory binary,
// this test binary IS that "current binary", so it must intercept the
// helper re-exec itself or the re-exec would run the test suite recursively
// instead of the real sandboxed target.
func TestMain(m *testing.M) {
	executor.MaybeApplyRlimitHelper()
	executor.MaybeApplySandboxHelper(networking.ServeDNSRelay)
	plugin.MaybeApplyPluginSandboxHelper()
	os.Exit(m.Run())
}

func TestRunDetect(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.py")
	if err := os.WriteFile(file, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"detect", file}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"kind": "python"`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestRunContainerUsesHardenedDefaultsAndForwardsArguments(t *testing.T) {
	var gotName string
	var gotArgs []string
	execute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runContainer([]string{"example.test/service@sha256:abc", "--serve"}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	want := []string{
		"run", "--rm", "--init", "--read-only", "--cap-drop=ALL",
		"--security-opt=no-new-privileges", "--network=none", "--cpus=1",
		"--memory=128m", "--pids-limit=128",
		"--tmpfs=/tmp:rw,noexec,nosuid,size=16m",
		"example.test/service@sha256:abc", "--serve",
	}
	if gotName != "docker" || !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("command = %s %v, want docker %v", gotName, gotArgs, want)
	}
}

func TestRunContainerAcceptsExplicitBoundedConfiguration(t *testing.T) {
	execute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		if name != "podman" || !strings.Contains(strings.Join(args, " "), "--network=bridge") {
			t.Fatalf("command = %s %v", name, args)
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runContainer([]string{
		"--runtime=podman", "--network=bridge", "--cpus=0.5",
		"--memory=64m", "--pids-limit=32", "service:local",
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunContainerNetworkConfiguration(t *testing.T) {
	execute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		joined := strings.Join(args, " ")
		for _, want := range []string{
			"--network=production", "--hostname=api.example",
			"--publish=127.0.0.1:8443:443/tcp", "--publish=[::1]:5353:53/udp",
			"--dns=1.1.1.1", "--add-host=database:10.0.0.5",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%s command missing %s: %v", name, want, args)
			}
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runContainer([]string{
		"--runtime=podman", "--network=production", "--hostname=api.example",
		"--publish=127.0.0.1:8443:443/tcp", "--publish=[::1]:5353:53/udp",
		"--dns=1.1.1.1", "--add-host=database:10.0.0.5", "service:local",
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunContainerRepeatedPortAliases(t *testing.T) {
	execute := func(_ string, args []string, _ io.Reader, _, _ io.Writer) error {
		joined := strings.Join(args, " ")
		for _, expected := range []string{
			"--publish=127.0.0.1:8080:80/tcp",
			"--publish=8443:443",
			"--publish=5353:53/udp",
		} {
			if !strings.Contains(joined, expected) {
				t.Fatalf("runtime args missing %s: %v", expected, args)
			}
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runContainer([]string{
		"--network=bridge",
		"-p", "127.0.0.1:8080:80/tcp",
		"--port", "8443:443",
		"-p=5353:53/udp",
		"service:local",
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunLaunchSelectsContainerOrMicroVM(t *testing.T) {
	// spec.Listen/Port default to 127.0.0.1:8080 with a single synthesized
	// TCP forward and one vCPU (see runMicroVM's spec-building code), so
	// whether this dispatches to the native KVM backend or to
	// run-microvm.sh/QEMU depends only on whether this host really has
	// native KVM - checked for real, not assumed.
	nativeAvailable := nativeKVMAvailableForTest(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	containerCalls := 0
	containerExecute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		containerCalls++
		if name != "docker" || args[len(args)-1] != "service:local" {
			t.Fatalf("container command=%s %v", name, args)
		}
		return nil
	}
	microVMCalls := 0
	microVMExecute := func(name string, args, _ []string, _ io.Reader, _, _ io.Writer) error {
		microVMCalls++
		if nativeAvailable {
			if name != self || len(args) < 2 || args[0] != "microvm" || args[1] != "__run-native" {
				t.Fatalf("microVM command=%s %v (expected native dispatch)", name, args)
			}
			return nil
		}
		if name != "scripts/microvm/run-microvm.sh" || args[0] != "/layout" {
			t.Fatalf("microVM command=%s %v (expected QEMU fallback)", name, args)
		}
		return nil
	}
	for _, args := range [][]string{
		{"--isolation=container", "service:local"},
		{"--isolation", "microvm", "--layout=/layout"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runLaunch(args, &stdout, &stderr, containerExecute, microVMExecute); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	if containerCalls != 1 || microVMCalls != 1 {
		t.Fatalf("containerCalls=%d microVMCalls=%d", containerCalls, microVMCalls)
	}
}

func TestIsolationFlagCanAppearAnywhere(t *testing.T) {
	if !hasIsolationFlag([]string{"service", "--isolation=container"}) ||
		!hasIsolationFlag([]string{"--network=none", "--isolation", "container"}) ||
		hasIsolationFlag([]string{"service"}) {
		t.Fatal("isolation flag detection failed")
	}
	var gotArgs []string
	execute := func(_ string, args []string, _ io.Reader, _, _ io.Writer) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runLaunch([]string{
		"--network=none", "service:local", "--isolation=container",
	}, &stdout, &stderr, execute, nil); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if gotArgs[len(gotArgs)-1] != "service:local" {
		t.Fatalf("runtime args=%v", gotArgs)
	}
}

func TestRunLaunchRejectsMissingIsolation(t *testing.T) {
	executeContainer := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("container executor called")
		return nil
	}
	executeMicroVM := func(string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("microVM executor called")
		return nil
	}
	for _, args := range [][]string{nil, {"--isolation=other"}, {"--isolation"}} {
		var stdout, stderr bytes.Buffer
		if code := runLaunch(args, &stdout, &stderr, executeContainer, executeMicroVM); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

func TestRunContainerRejectsUnsafeOrInvalidOptions(t *testing.T) {
	execute := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("executor called for invalid arguments")
		return nil
	}
	for _, args := range [][]string{
		nil,
		{"--runtime=sh", "image"},
		{"--network=host", "image"},
		{"--network=none", "--publish=8080:80", "image"},
		{"--network=bridge", "--publish=bad", "image"},
		{"--network=bridge", "--dns=resolver", "image"},
		{"--network=bridge", "--hostname=../bad", "image"},
		{"--network=bridge", "--add-host=bad", "image"},
		{"--cpus=0", "image"},
		{"--memory=", "image"},
		{"--pids-limit=0", "image"},
		{"--image"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runContainer(args, &stdout, &stderr, execute); code != 2 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
}

func TestRunMicroVMNative(t *testing.T) {
	var command string
	var args, environment []string
	execute := func(name string, commandArgs, env []string, _ io.Reader, _, _ io.Writer) error {
		command = name
		args = append([]string(nil), commandArgs...)
		environment = append([]string(nil), env...)
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"run", "--backend=native", "--name=demo", "--layout=/verified/layout",
		"--memory-mib=256", "--vcpus=2", "--port=9090",
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if command != "scripts/microvm/run-microvm.sh" ||
		!reflect.DeepEqual(args, []string{"/verified/layout", "9090"}) ||
		!reflect.DeepEqual(environment, []string{
			"MICROVM_MEMORY=256M", "MICROVM_SMP=2", "MICROVM_HOST_ADDRESS=127.0.0.1",
			"MICROVM_FORWARDS=tcp|127.0.0.1|9090|9090",
		}) {
		t.Fatalf("command=%s args=%v environment=%v", command, args, environment)
	}
}

// TestNativeKVMEligible calls the real nativeKVMEligible against this
// process's real host - no faked probe, no faked GOOS/GOARCH. Conditions
// this environment cannot itself produce for real (a non-linux/amd64 host,
// no /dev/kvm at all) are not asserted here; the arch check and the probe
// call are each a single, easily-read comparison, and the fallback path
// they feed is exercised for real by the QEMU-side tests below on
// whichever host actually lacks native KVM.
func TestNativeKVMEligible(t *testing.T) {
	baseSpec := func() microvm.Spec {
		return microvm.Spec{VCPUs: 1, Forwards: []networking.Forward{{Protocol: "tcp", HostPort: 8080, GuestPort: 8080}}}
	}

	t.Run("multiple vCPUs falls back", func(t *testing.T) {
		spec := baseSpec()
		spec.VCPUs = 2
		if ok, reason := nativeKVMEligible(context.Background(), spec); ok || reason == "" {
			t.Fatalf("expected ineligible with a reason, ok=%v reason=%q", ok, reason)
		}
	})

	t.Run("eligible if and only if this host really has native KVM", func(t *testing.T) {
		want := nativeKVMAvailableForTest(t)
		ok, reason := nativeKVMEligible(context.Background(), baseSpec())
		if ok != want {
			t.Fatalf("nativeKVMEligible=%v reason=%q, want %v (real ProbeNative.Available)", ok, reason, want)
		}
	})

	t.Run("UDP forward falls back", func(t *testing.T) {
		if !nativeKVMAvailableForTest(t) {
			t.Skip("this host has no native KVM; the earlier probe check would already reject before the forward protocol is ever inspected")
		}
		spec := baseSpec()
		spec.Forwards = []networking.Forward{{Protocol: "udp", HostPort: 53, GuestPort: 53}}
		if ok, _ := nativeKVMEligible(context.Background(), spec); ok {
			t.Fatal("expected ineligible with a UDP forward")
		}
	})
}

func TestRunMicroVMNativeKVMDispatch(t *testing.T) {
	if !nativeKVMAvailableForTest(t) {
		t.Skip("this host has no native KVM; run/start would fall back to run-microvm.sh, covered by TestRunMicroVMNative")
	}
	var command string
	var args []string
	execute := func(name string, commandArgs, _ []string, _ io.Reader, _, _ io.Writer) error {
		command = name
		args = append([]string(nil), commandArgs...)
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"run", "--backend=native", "--name=demo", "--layout=/verified/layout", "--memory-mib=256",
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if command != self || len(args) < 2 || args[0] != "microvm" || args[1] != "__run-native" {
		t.Fatalf("command=%s args=%v", command, args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--layout /verified/layout") || !strings.Contains(joined, "--memory-mib 256") {
		t.Fatalf("args=%v", args)
	}
	if !strings.Contains(stderr.String(), "using native KVM backend") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestRunMicroVMMultipleNetworkForwards(t *testing.T) {
	execute := func(_ string, _, environment []string, _ io.Reader, _, _ io.Writer) error {
		joined := strings.Join(environment, " ")
		if !strings.Contains(joined, "tcp|127.0.0.1|8080|80") ||
			!strings.Contains(joined, "udp|::1|5353|53") {
			t.Fatalf("environment=%v", environment)
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"run", "--layout=/verified/layout",
		"--publish=127.0.0.1:8080:80/tcp", "--publish=[::1]:5353:53/udp",
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunMicroVMMultiplePortAliases(t *testing.T) {
	execute := func(_ string, _, environment []string, _ io.Reader, _, _ io.Writer) error {
		joined := strings.Join(environment, " ")
		for _, expected := range []string{
			"tcp|127.0.0.1|8080|80",
			"tcp|127.0.0.1|8443|443",
			"udp|127.0.0.1|5353|53",
		} {
			if !strings.Contains(joined, expected) {
				t.Fatalf("environment missing %s: %v", expected, environment)
			}
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"run", "--layout=/verified/layout",
		"-p", "8080:80", "--port=8443:443", "-p=5353:53/udp",
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunMicroVMNativeLifecycleUsesSystemd(t *testing.T) {
	var calls [][]string
	execute := func(name string, args, _ []string, _ io.Reader, _, _ io.Writer) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	for _, args := range [][]string{
		{"start", "--backend=native", "--name=demo", "--layout=/verified/layout"},
		{"status", "--backend=native", "--name=demo"},
		{"logs", "--backend=native", "--name=demo"},
		{"restart", "--backend=native", "--name=demo"},
		{"stop", "--backend=native", "--name=demo"},
		{"delete", "--backend=native", "--name=demo"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runMicroVM(args, &stdout, &stderr, execute); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	if len(calls) != 7 || calls[0][0] != "systemd-run" ||
		!strings.Contains(strings.Join(calls[0], " "), "platform-factory-microvm-demo.service") {
		t.Fatalf("calls=%v", calls)
	}
}

func TestRunMicroVMKubeVirtLifecycle(t *testing.T) {
	var calls [][]string
	execute := func(name string, args, _ []string, _ io.Reader, _, _ io.Writer) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	for _, action := range []string{"start", "stop", "restart", "status", "logs", "delete"} {
		var stdout, stderr bytes.Buffer
		code := runMicroVM([]string{
			action, "--backend=kubevirt", "--name=demo", "--namespace=production",
		}, &stdout, &stderr, execute)
		if code != 0 {
			t.Fatalf("%s code=%d stderr=%s", action, code, stderr.String())
		}
	}
	if len(calls) != 6 ||
		calls[0][0] != "platform-factory-kubevirt" || calls[0][1] != "start" ||
		!strings.Contains(strings.Join(calls[0], " "), "--namespace production") ||
		!strings.Contains(strings.Join(calls[0], " "), "--name demo") {
		t.Fatalf("calls=%v", calls)
	}
}

// The main module never renders a KubeVirt manifest or validates
// KubeVirt-specific fields itself - both live in the plugins/kubevirt
// module, which cmd/platform-factory only ever drives as a subprocess (see
// runKubeVirt). This test verifies the forwarding shape; manifest
// rendering and validation are covered by
// plugins/kubevirt/cmd/platform-factory-kubevirt's own tests.
func TestRunMicroVMKubeVirtCreateForwardsToPlugin(t *testing.T) {
	var command string
	var args []string
	execute := func(name string, commandArgs, _ []string, _ io.Reader, _, _ io.Writer) error {
		command, args = name, commandArgs
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"create", "--backend=kubevirt", "--name=demo",
		"--image=registry.example/boot@sha256:" + strings.Repeat("b", 64),
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if command != "platform-factory-kubevirt" || args[0] != "create" {
		t.Fatalf("command=%s args=%v", command, args)
	}
	for _, arg := range args {
		if arg == "--apply" {
			t.Fatalf("--apply forwarded without being requested: args=%v", args)
		}
	}
}

func TestRunMicroVMKubeVirtCreateForwardsApplyAndPublish(t *testing.T) {
	var args []string
	execute := func(_ string, commandArgs, _ []string, _ io.Reader, _, _ io.Writer) error {
		args = commandArgs
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"create", "--backend=kubevirt", "--name=demo", "--apply",
		"--image=registry.example/boot@sha256:" + strings.Repeat("b", 64),
		"-p", "8080:80",
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--apply") || !strings.Contains(joined, "--publish 8080:80") {
		t.Fatalf("args=%v", args)
	}
}

func TestRunMicroVMPackage(t *testing.T) {
	var command string
	var args []string
	execute := func(name string, commandArgs, _ []string, _ io.Reader, _, _ io.Writer) error {
		command = name
		args = append([]string(nil), commandArgs...)
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"package", "--layout=/verified/layout", "--output=/tmp/boot-context",
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if command != "scripts/microvm/prepare-kubevirt-boot.sh" ||
		!reflect.DeepEqual(args, []string{"/verified/layout", "/tmp/boot-context"}) {
		t.Fatalf("command=%s args=%v", command, args)
	}
}

func TestRunMicroVMRejectsInvalidConfiguration(t *testing.T) {
	execute := func(string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("executor called for invalid configuration")
		return nil
	}
	for _, args := range [][]string{
		nil,
		{"package", "--layout=/layout"},
		{"run", "--backend=native"},
		{"create", "--backend=native", "--layout=/layout"},
		{"run", "--backend=other", "--layout=/layout"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runMicroVM(args, &stdout, &stderr, execute); code == 0 {
			t.Fatalf("args=%v unexpectedly succeeded", args)
		}
	}
}

// A kubevirt spec platform-factory-kubevirt would itself reject (an unpinned
// image, here) is no longer caught by cmd/platform-factory - that validation now
// lives entirely in the plugin. This verifies the plugin's non-zero exit
// still propagates end-to-end.
func TestRunMicroVMKubeVirtPropagatesPluginRejection(t *testing.T) {
	execute := func(string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		return microvmExitError{code: 2}
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"create", "--backend=kubevirt", "--name=demo", "--image=latest",
	}, &stdout, &stderr, execute)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

type microvmExitError struct{ code int }

func (e microvmExitError) Error() string { return "exit error" }
func (e microvmExitError) ExitCode() int { return e.code }

func TestRunRejectsUsageAndMissingInput(t *testing.T) {
	for _, args := range [][]string{{"unknown"}, {"detect"}, {"detect", "--bad"}, {"detect", "/missing"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Fatalf("args=%v unexpectedly succeeded", args)
		}
	}
}

func TestRunHelpVersionAndAliases(t *testing.T) {
	for _, args := range [][]string{
		nil, {"help"}, {"--help"}, {"build", "--help"}, {"detect", "--help"},
		{"inspect", "--help"}, {"vm", "--help"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
		if stdout.Len() == 0 && stderr.Len() == 0 {
			t.Fatalf("args=%v produced no help", args)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "platform-factory ") {
		t.Fatalf("version code=%d stdout=%s", code, stdout.String())
	}
	for input, want := range map[string]string{
		"image build":   "build",
		"image compose": "compose",
		"image publish": "publish",
		"container run": "run",
		"vm status":     "microvm",
	} {
		got, warning := commandAlias(strings.Fields(input))
		if len(got) == 0 || got[0] != want {
			t.Fatalf("commandAlias(%q)=%v", input, got)
		}
		if warning == "" {
			t.Fatalf("commandAlias(%q): expected a deprecation warning", input)
		}
	}
}

func TestRunRequiresExplicitAmbiguityAcceptance(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "package-lock.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "requirements.lock"), []byte(""), 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"detect", root}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"detect", "--accept-ambiguous", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunInspectAndVerifyRejectInvalidLayout(t *testing.T) {
	for _, command := range []string{"inspect", "verify"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{command, t.TempDir()}, &stdout, &stderr); code != 1 {
			t.Fatalf("%s code=%d stderr=%s", command, code, stderr.String())
		}
	}
}

func TestRunInspectAndVerifyValidLayout(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	_ = os.WriteFile(binary, []byte("payload"), 0o755)
	layout := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{Binary: binary, Output: layout}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"inspect", "verify"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{command, layout}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s code=%d stderr=%s", command, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), `"valid": true`) {
			t.Fatalf("%s output=%s", command, stdout.String())
		}
	}
}

func TestRunBuildThenVerify(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("test executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--output", output, "--arch", "arm64", binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("build code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"architecture": "arm64"`) ||
		!strings.Contains(stdout.String(), `"valid": true`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"verify", output}, &stdout, &stderr); code != 0 {
		t.Fatalf("verify code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunImageBuildWithUnifiedOptions(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("test executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"image", "build", "-o", output, "--platform", "linux/arm64",
		"--image", "example/api", "--tag", "v2", "--entrypoint", "/srv/api",
		"--profile", "static", "--label", "org.example.stage=production",
		"--created", "2026-07-27T00:00:00Z", binary,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{
		`"platform": "linux/arm64"`,
		`"reference": "example/api:v2"`,
		`"valid": true`,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %s: %s", want, stdout.String())
		}
	}
	report, err := layout.Verify(output)
	if err != nil || report.Platforms[0].Reference != "example/api:v2" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func layoutLayerCount(t *testing.T, layoutPath string) int {
	t.Helper()
	indexData, err := os.ReadFile(filepath.Join(layoutPath, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(indexData, &index); err != nil || len(index.Manifests) == 0 {
		t.Fatalf("index=%s err=%v", indexData, err)
	}
	manifestData, err := os.ReadFile(filepath.Join(layoutPath, "blobs", "sha256",
		strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	return len(manifest.Layers)
}

func TestRunBuildDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--dry-run", "--output", output, binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"dry_run": true`, `"profile": "static"`, `"valid": true`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %s: %s", want, stdout.String())
		}
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the layout: %v", err)
	}
}

func TestRunDetectAndInspectTextFormat(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	layoutPath := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{Binary: binary, Output: layoutPath}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"detect", "--format", "text", binary}, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "unknown") {
		t.Fatalf("detect code=%d stdout=%s", code, stdout.String())
	}
	stdout.Reset()
	if code := run([]string{"verify", "--format", "text", layoutPath}, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "valid: 1 manifests") {
		t.Fatalf("verify code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if code := run([]string{"inspect", "--format", "yaml", layoutPath}, &stdout, &stderr); code != 2 {
		t.Fatalf("bad format code=%d", code)
	}
	if code := run([]string{"detect", "--format", "yaml", binary}, &stdout, &stderr); code != 2 {
		t.Fatalf("bad detect format code=%d", code)
	}
}

func TestRunDiffCommand(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	layoutA := filepath.Join(root, "a")
	layoutB := filepath.Join(root, "b")
	layoutC := filepath.Join(root, "c")
	for _, build := range []struct {
		output string
		env    map[string]string
	}{
		{layoutA, nil},
		{layoutB, nil},
		{layoutC, map[string]string{"MODE": "debug"}},
	} {
		if _, err := oci.Build(oci.Options{Binary: binary, Output: build.output, Env: build.env}); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"diff", layoutA, layoutB}, &stdout, &stderr); code != 0 {
		t.Fatalf("identical code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"equal": true`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	stdout.Reset()
	if code := run([]string{"diff", "--format", "text", layoutA, layoutC}, &stdout, &stderr); code != 1 {
		t.Fatalf("divergent code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "config env") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if code := run([]string{"diff", layoutA, t.TempDir()}, &stdout, &stderr); code != 2 {
		t.Fatalf("invalid code=%d", code)
	}
	if code := run([]string{"diff", layoutA}, &stdout, &stderr); code != 2 {
		t.Fatalf("usage code=%d", code)
	}
	if code := run([]string{"diff", "--format", "yaml", layoutA, layoutB}, &stdout, &stderr); code != 2 {
		t.Fatalf("format code=%d", code)
	}
}

func TestRunBuildRebuildVerifiesReproducibility(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("reproducible payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--rebuild=3", "--require-identical", "--output", output, binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"reproducible": true`, `"rebuilds": 3`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %s: %s", want, stdout.String())
		}
	}
	if _, err := layout.Verify(output); err != nil {
		t.Fatalf("installed layout does not verify: %v", err)
	}
	// A single-platform rebuild only; multi-platform is rejected.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"build", "--rebuild=2", "--platform", "linux/amd64=" + binary,
		"--platform", "linux/arm64=" + binary, "--output", filepath.Join(root, "multi"),
	}, &stdout, &stderr); code != 2 {
		t.Fatalf("multi-platform rebuild code=%d", code)
	}
}

func TestRunBuildRebuildTextAndExistingOutput(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--rebuild=2", "--format", "text", "--output", output, binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "reproducible: 2 rebuilds") {
		t.Fatalf("stdout=%s", stdout.String())
	}
	// A second rebuild into the same (now existing) output is refused.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"build", "--rebuild=2", "--output", output, binary}, &stdout, &stderr); code != 1 {
		t.Fatalf("existing output code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestEmitRebuildResultDivergence(t *testing.T) {
	// Build two genuinely different layouts so layout.Diff yields a real,
	// non-empty divergence report, then drive emitRebuildResult's
	// not-reproducible branches directly.
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	if _, err := oci.Build(oci.Options{Binary: binary, Output: a}); err != nil {
		t.Fatal(err)
	}
	if _, err := oci.Build(oci.Options{Binary: binary, Output: b, Env: map[string]string{"MODE": "debug"}}); err != nil {
		t.Fatal(err)
	}
	report, err := layout.Diff(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if report.Equal {
		t.Fatal("expected a divergence for the test setup")
	}
	outcome := rebuildOutcome{
		reference: "example/service:v1", platform: "linux/amd64", rebuilds: 2,
		digest: "sha256:x", output: filepath.Join(root, "out"),
		divergences: []layout.DiffReport{report}, requireIdentical: true,
	}
	var stdout, stderr bytes.Buffer
	if code := emitRebuildResult(outcome, "json", &stdout, &stderr); code != 1 {
		t.Fatalf("require-identical divergence code=%d", code)
	}
	if !strings.Contains(stdout.String(), `"reproducible": false`) || !strings.Contains(stdout.String(), `"divergences"`) {
		t.Fatalf("json stdout=%s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	outcome.requireIdentical = false
	if code := emitRebuildResult(outcome, "text", &stdout, &stderr); code != 0 {
		t.Fatalf("report-only divergence code=%d", code)
	}
	if !strings.Contains(stdout.String(), "NOT reproducible") {
		t.Fatalf("text stdout=%s", stdout.String())
	}
}

func TestRunBuildRebuildSurfacesBuildError(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	// A missing input makes the underlying build fail; the rebuild loop
	// must surface it rather than install a partial layout.
	if code := run([]string{"build", "--rebuild=2", "--output", filepath.Join(root, "out"), filepath.Join(root, "missing")}, &stdout, &stderr); code == 0 {
		t.Fatalf("missing input rebuild unexpectedly succeeded: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, "out")); !os.IsNotExist(err) {
		t.Fatalf("failed rebuild left an output: %v", err)
	}
}

func TestRunBuildRebuildRejectsInvalidCount(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	// --rebuild=1 is the normal single build (no verification).
	if code := run([]string{"build", "--rebuild=1", "--output", filepath.Join(root, "single"), binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("rebuild=1 code=%d stderr=%s", code, stderr.String())
	}
	if code := run([]string{"build", "--rebuild=0", "--output", filepath.Join(root, "zero"), binary}, &stdout, &stderr); code != 2 {
		t.Fatalf("rebuild=0 should be rejected, code=%d", code)
	}
}

func TestRunBuildSemanticLayersFlag(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	toolchain := filepath.Join(root, "runtime")
	for _, name := range []string{binary, toolchain} {
		if err := os.WriteFile(name, []byte("payload "+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	layered := filepath.Join(root, "layered")
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"build", "--output", layered, "--semantic-layers",
		"--extra-file", "toolchain@/opt/runtime=" + toolchain, binary,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("build code=%d stderr=%s", code, stderr.String())
	}
	if _, err := layout.Verify(layered); err != nil {
		t.Fatal(err)
	}
	if got := layoutLayerCount(t, layered); got != 2 {
		t.Fatalf("semantic layers=%d, want 2 (toolchain, application)", got)
	}
	flat := filepath.Join(root, "flat")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"build", "--output", flat,
		"--extra-file", "toolchain@/opt/runtime=" + toolchain, binary,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("flat build code=%d stderr=%s", code, stderr.String())
	}
	if got := layoutLayerCount(t, flat); got != 1 {
		t.Fatalf("default layers=%d, want 1", got)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"build", "--output", filepath.Join(root, "bad"), "--semantic-layers",
		"--extra-file", "warehouse@/opt/runtime=" + toolchain, binary,
	}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown category code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunBuildMultiPlatformInOneCommand(t *testing.T) {
	root := t.TempDir()
	amd64 := filepath.Join(root, "service-amd64")
	arm64 := filepath.Join(root, "service-arm64")
	if err := os.WriteFile(amd64, []byte("amd64 executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(arm64, []byte("arm64 executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "multi")
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"build", "-o", output,
		"--image", "example/service", "--tag", "v3",
		"--platform", "linux/amd64=" + amd64,
		"--platform", "linux/arm64=" + arm64,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	report, err := layout.Verify(output)
	if err != nil {
		t.Fatal(err)
	}
	if report.Manifests != 2 || len(report.Platforms) != 2 {
		t.Fatalf("report=%+v", report)
	}
	for _, value := range []string{`"linux/amd64"`, `"linux/arm64"`, `"example/service:v3"`} {
		if !strings.Contains(stdout.String(), value) {
			t.Fatalf("stdout missing %s: %s", value, stdout.String())
		}
	}
}

func TestBuildTargetsRejectsAmbiguousSyntax(t *testing.T) {
	for _, test := range []struct {
		platforms, positional []string
	}{
		{nil, nil},
		{[]string{"bad"}, []string{"app"}},
		{[]string{"linux/amd64=app"}, nil},
		{[]string{"linux/amd64=app", "linux/arm64"}, nil},
		{[]string{"linux/amd64=app", "linux/arm64=other"}, []string{"extra"}},
	} {
		if _, _, err := buildTargets(test.platforms, test.positional, "linux", "amd64"); err == nil {
			t.Fatalf("accepted platforms=%v positional=%v", test.platforms, test.positional)
		}
	}
}

func TestBuildAndComposeTextOutput(t *testing.T) {
	root := t.TempDir()
	build := func(name, architecture string) string {
		binary := filepath.Join(root, name)
		if err := os.WriteFile(binary, []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(root, name+"-layout")
		var stdout, stderr bytes.Buffer
		if code := run([]string{
			"build", "--format", "text", "--arch", architecture, "-o", output, binary,
		}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "built ") {
			t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
		return output
	}
	amd64 := build("text-amd64", "amd64")
	arm64 := build("text-arm64", "arm64")
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"compose", "--format", "text", "-o", filepath.Join(root, "catalog"), amd64, arm64,
	}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "composed 2 manifests") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunComposeMultiArchitecture(t *testing.T) {
	root := t.TempDir()
	build := func(name, architecture string) string {
		binary := filepath.Join(root, name)
		if err := os.WriteFile(binary, []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(root, name+"-layout")
		if _, err := oci.Build(oci.Options{
			Binary: binary, Output: output, Architecture: architecture,
			ImageName: "example/service", Tag: "v1",
		}); err != nil {
			t.Fatal(err)
		}
		return output
	}
	amd64 := build("amd64", "amd64")
	arm64 := build("arm64", "arm64")
	output := filepath.Join(root, "multi")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"compose", "--output", output, amd64, arm64}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"manifests": 2`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if code := run([]string{"compose", "--output", filepath.Join(root, "invalid"), amd64}, &stdout, &stderr); code != 2 {
		t.Fatalf("single input code=%d", code)
	}
}

func TestRunBuildWithConfigAndErrors(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	_ = os.WriteFile(binary, []byte("payload"), 0o755)
	config := filepath.Join(root, "config.json")
	_ = os.WriteFile(config, []byte(`{"entrypoint":"/opt/service","profile":"static","user":"10001:10001"}`), 0o600)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--config", config, "--output", filepath.Join(root, "configured"), binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, args := range [][]string{
		{"build"},
		{"build", "--bad", binary},
		{"build", "--config", filepath.Join(root, "missing"), binary},
		{"build", "--extra-file", "bad", binary},
		{"build", filepath.Join(root, "missing")},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Fatalf("args=%v unexpectedly succeeded", args)
		}
	}
}

func TestMicroVMProbeProducesMachineReadableDiagnostics(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{"probe"}, &stdout, &stderr, nil)
	if code != 0 && code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var result struct {
		Available    bool              `json:"available"`
		Architecture string            `json:"architecture"`
		Details      map[string]string `json:"details"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid probe JSON: %v: %s", err, stdout.String())
	}
	if result.Architecture == "" || result.Details["backend"] == "" {
		t.Fatalf("probe=%+v", result)
	}
	if code == 0 != result.Available {
		t.Fatalf("code=%d available=%v", code, result.Available)
	}
}

func TestRunMicroVMRunLegacyDiskRequiresDiskFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{"run-legacy-disk"}, &stdout, &stderr, nil)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--disk is required") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestRunMicroVMRunLegacyDiskFailsClosedOnUnrecognizedFormat(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "not-a-disk.bin")
	if err := os.WriteFile(disk, bytes.Repeat([]byte{0x42}, 512), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	execute := func(name string, args, environment []string, _ io.Reader, _, _ io.Writer) error {
		t.Fatalf("runner should never be invoked for an unrecognized format; name=%s args=%v", name, args)
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{"run-legacy-disk", "--disk=" + disk}, &stdout, &stderr, execute)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unrecognized or unsupported disk image format") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestRunMicroVMRunLegacyDiskDetectsFormatAndInvokesRunner(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "legacy.qcow2")
	header := append([]byte{0x51, 0x46, 0x49, 0xfb, 0x00, 0x00, 0x00, 0x03}, make([]byte, 504)...)
	if err := os.WriteFile(disk, header, 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	var gotName string
	var gotArgs []string
	execute := func(name string, args, environment []string, _ io.Reader, _, _ io.Writer) error {
		gotName, gotArgs = name, args
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{"run-legacy-disk", "--disk=" + disk}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if gotName != "scripts/microvm/run-legacy-disk.sh" {
		t.Fatalf("runner=%s", gotName)
	}
	if len(gotArgs) != 2 || gotArgs[0] != disk || gotArgs[1] != "qcow2" {
		t.Fatalf("args=%v", gotArgs)
	}
	if !strings.Contains(stderr.String(), "boot disk="+disk) || !strings.Contains(stderr.String(), "format=qcow2") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestRunMicroVMRunLegacyDiskMultiDiskUsesBootDiskOverride(t *testing.T) {
	osDisk := filepath.Join(t.TempDir(), "os.raw")
	osHeader := make([]byte, 4096)
	osHeader[446] = 0x80 // active/bootable
	osHeader[446+4] = 0x83
	osHeader[510], osHeader[511] = 0x55, 0xaa
	if err := os.WriteFile(osDisk, osHeader, 0o644); err != nil {
		t.Fatalf("write os disk: %v", err)
	}
	dataDisk := filepath.Join(t.TempDir(), "data.raw")
	dataHeader := make([]byte, 4096)
	dataHeader[510], dataHeader[511] = 0x55, 0xaa // valid MBR, no active partition
	if err := os.WriteFile(dataDisk, dataHeader, 0o644); err != nil {
		t.Fatalf("write data disk: %v", err)
	}

	var gotArgs []string
	execute := func(name string, args, environment []string, _ io.Reader, _, _ io.Writer) error {
		gotArgs = args
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"run-legacy-disk", "--disk=" + dataDisk, "--disk=" + osDisk,
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	// osDisk is auto-detected as the boot disk (the only one with an
	// active MBR partition) even though it was given second.
	if len(gotArgs) != 4 || gotArgs[0] != osDisk || gotArgs[1] != "raw" || gotArgs[2] != dataDisk || gotArgs[3] != "raw" {
		t.Fatalf("args=%v", gotArgs)
	}
	if !strings.Contains(stderr.String(), "boot disk="+osDisk) {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestRunMicroVMRunLegacyDiskMultiDiskAmbiguousWithoutOverride(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first.raw")
	second := filepath.Join(t.TempDir(), "second.raw")
	for _, path := range []string{first, second} {
		header := make([]byte, 4096)
		header[510], header[511] = 0x55, 0xaa // valid MBR, neither disk marked bootable
		if err := os.WriteFile(path, header, 0o644); err != nil {
			t.Fatalf("write disk: %v", err)
		}
	}
	execute := func(name string, args, environment []string, _ io.Reader, _, _ io.Writer) error {
		t.Fatalf("runner should never be invoked when the boot disk is ambiguous; args=%v", args)
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"run-legacy-disk", "--disk=" + first, "--disk=" + second,
	}, &stdout, &stderr, execute)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--boot-disk") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestCommandAliasHandlesEmptyInput(t *testing.T) {
	got, warning := commandAlias(nil)
	if len(got) != 0 || warning != "" {
		t.Fatalf("commandAlias(nil)=%v, want empty", got)
	}
}

func TestRunInspectHandlesHelpAndUsageDirectly(t *testing.T) {
	// run()'s own "COMMAND -h/--help" shortcut intercepts the exact
	// two-argument form before it ever reaches runInspect, so runInspect's
	// own flag-parse ErrHelp and NArg branches need a direct call (or a
	// third argument) to be exercised at all.
	var stdout, stderr bytes.Buffer
	if code := runInspect("inspect", []string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--help code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runInspect("inspect", nil, &stdout, &stderr); code != 2 {
		t.Fatalf("missing layout code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunDetectSurfacesPluginStartFailure(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "app")
	if err := os.WriteFile(file, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"detect", "--plugin-dir", root, "--plugin-key", filepath.Join(root, "missing.pem"), file,
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "platform-factory detect:") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestTopLevelDispatchCoversRemainingCommands drives every run() dispatch
// branch that no other test reaches through the top-level entry point:
// evidence, publish, run (with and without --isolation), launch --publish,
// and launch's doubled --dry-run/--plan rejection.
func TestTopLevelDispatchCoversRemainingCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"evidence"}, &stdout, &stderr); code == 0 {
		t.Fatalf("evidence with no arguments unexpectedly succeeded: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"publish"}, &stdout, &stderr); code != 2 {
		t.Fatalf("publish with no arguments code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"run"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run with no arguments code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"run", "--isolation=container"}, &stdout, &stderr); code == 0 {
		t.Fatalf("run --isolation without a target unexpectedly succeeded: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"launch", "--publish"}, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "pass --yes") {
		t.Fatalf("launch --publish code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"launch", "--dry-run", "--plan"}, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "platform-factory launch:") {
		t.Fatalf("launch doubled plan flag code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunContainerHandlesHelpAndDashPrefixedImage(t *testing.T) {
	execute := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("executor called")
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runContainer([]string{"--help"}, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("--help code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runContainer([]string{"--network=bridge", "--", "-badimage"}, &stdout, &stderr, execute); code != 2 ||
		!strings.Contains(stderr.String(), "invalid image reference") {
		t.Fatalf("dash-prefixed image code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunComposeHelpUsageAndFormat(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runCompose([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--help code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runCompose([]string{"only-one-layout"}, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "usage: platform-factory compose") {
		t.Fatalf("too few layouts code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runCompose([]string{"--format", "yaml", "a", "b"}, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "format must be json or text") {
		t.Fatalf("bad format code=%d stderr=%s", code, stderr.String())
	}
}

// TestBuildTargetsAndResolveBuildTargetRemainingErrorBranches closes the
// few buildTargets/resolveBuildTarget error paths not already exercised
// through TestBuildTargetsRejectsAmbiguousSyntax and the successful build
// tests: a single --platform without exactly one executable, an invalid
// platform inside multi-platform syntax, and a non-executable input
// (detected as a different, unambiguous kind) reaching resolveBuildTarget.
func TestBuildTargetsAndResolveBuildTargetRemainingErrorBranches(t *testing.T) {
	if _, code, err := buildTargets([]string{"linux/amd64"}, nil, "linux", "amd64"); err == nil || code != 2 {
		t.Fatalf("single platform without executable: code=%d err=%v", code, err)
	}
	if _, code, err := buildTargets(
		[]string{"bogus/arch=a", "linux/amd64=b"}, nil, "linux", "amd64",
	); err == nil || code != 2 {
		t.Fatalf("invalid platform in multi-platform syntax: code=%d err=%v", code, err)
	}
	root := t.TempDir()
	script := filepath.Join(root, "app.py")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveBuildTarget(buildTarget{os: "linux", architecture: "amd64", input: script}, buildSettings{}); err == nil {
		t.Fatal("expected a non-ELF, non-unknown input to be rejected")
	}
}

func TestCommandExecutorsPropagateProcessSuccessAndFailure(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := executeContainerRuntime(executable, []string{"-test.run=^$"}, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := executeMicroVMCommand(executable, []string{"-test.run=^$"},
		[]string{"PLATFORM_FACTORY_EXECUTOR_TEST=1"}, nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := executeProjectCommand(executable, []string{"-test.run=^$"},
		t.TempDir(), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := executeContainerRuntime(filepath.Join(t.TempDir(), "missing"), nil,
		nil, &stdout, &stderr); err == nil {
		t.Fatal("missing executable succeeded")
	}
}

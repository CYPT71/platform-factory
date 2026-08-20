package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	buildapp "github.com/CYPT71/platform-factory/internal/app/build"
	microvmapp "github.com/CYPT71/platform-factory/internal/app/microvm"
	"github.com/CYPT71/platform-factory/internal/attestation"
	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/dockersave"
	"github.com/CYPT71/platform-factory/internal/executor"
	"github.com/CYPT71/platform-factory/internal/hypervisor"
	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/microvm"
	"github.com/CYPT71/platform-factory/internal/networking"
	"github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/internal/signing"
	"github.com/CYPT71/platform-factory/internal/vmdisk"
	"github.com/CYPT71/platform-factory/oci"
	api "github.com/CYPT71/platform-factory/sdk/plugin"
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
	if existing := os.Getenv("PLATFORM_FACTORY_LANG_PLUGIN_DIR"); existing != "" {
		if info, statErr := os.Stat(existing); statErr == nil && info.IsDir() {
			os.Exit(m.Run())
		}
	}
	pluginDir, err := os.MkdirTemp("", "platform-factory-test-language-plugins-*")
	if err != nil {
		panic(err)
	}
	for _, language := range []string{"go", "python", "node", "rust"} {
		name := "platform-factory-lang-" + language
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		command := exec.Command("go", "build", "-o", filepath.Join(pluginDir, name), ".")
		command.Dir = filepath.Join("..", "..", "plugins", "lang-"+language)
		if output, buildErr := command.CombinedOutput(); buildErr != nil {
			panic(fmt.Sprintf("build %s test plugin: %v: %s", language, buildErr, output))
		}
	}
	_ = os.Setenv("PLATFORM_FACTORY_LANG_PLUGIN_DIR", pluginDir)
	code := m.Run()
	_ = os.RemoveAll(pluginDir)
	os.Exit(code)
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
	if !strings.Contains(stdout.String(), `"kind": "script"`) || !strings.Contains(stdout.String(), `"interpreter": "/usr/bin/env python3"`) {
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
	code := runContainer(context.Background(), []string{"example.test/service@sha256:abc", "--serve"}, &stdout, &stderr, execute)
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
	code := runContainer(context.Background(), []string{
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
	code := runContainer(context.Background(), []string{
		"--runtime=podman", "--network=production", "--hostname=api.example",
		"--publish=127.0.0.1:8443:443/tcp", "--publish=[::1]:5353:53/udp",
		"--dns=1.1.1.1", "--add-host=database:10.0.0.5", "service:local",
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunContainerVolumesAndEnv(t *testing.T) {
	execute := func(name string, args []string, _ io.Reader, _, _ io.Writer) error {
		joined := strings.Join(args, " ")
		for _, want := range []string{
			"--volume=/host/data:/data:ro", "--volume=/host/cache:/cache",
			"--env=LOG_LEVEL=debug", "--env=INHERITED",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%s command missing %s: %v", name, want, args)
			}
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runContainer(context.Background(), []string{
		"--volume=/host/data:/data:ro", "-v=/host/cache:/cache",
		"--env=LOG_LEVEL=debug", "-e=INHERITED", "service:local",
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
	code := runContainer(context.Background(), []string{
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
		if code := runLaunch(context.Background(), args, &stdout, &stderr, containerExecute, microVMExecute); code != 0 {
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
	if code := runLaunch(context.Background(), []string{
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
		if code := runLaunch(context.Background(), args, &stdout, &stderr, executeContainer, executeMicroVM); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}

// TestValidVolumeSpec covers validVolumeSpec's branches directly:
// TestRunContainerRejectsUnsafeOrInvalidOptions only exercises the
// too-few-parts and empty-host cases through the full CLI; a NUL byte
// and an empty container path are covered here since a NUL byte cannot
// travel through a shell-style os.Args string in a realistic CLI test.
func TestValidVolumeSpec(t *testing.T) {
	cases := []struct {
		value   string
		wantErr bool
	}{
		{"/host/data:/data", false},
		{"/host/data:/data:ro", false},
		{"/host/data:/data:ro\x00", true},
		{"bad", true},
		{":/container", true},
		{"/host/data:", true},
	}
	for _, c := range cases {
		err := validVolumeSpec(c.value)
		if (err != nil) != c.wantErr {
			t.Errorf("validVolumeSpec(%q) error=%v, wantErr=%v", c.value, err, c.wantErr)
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
		{"--volume=bad", "image"},
		{"--volume=:/container", "image"},
		{"--env=", "image"},
		{"--env==novalue", "image"},
		{"--cpus=0", "image"},
		{"--memory=", "image"},
		{"--pids-limit=0", "image"},
		{"--image"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runContainer(context.Background(), args, &stdout, &stderr, execute); code != 2 {
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

// TestRunMicroVMRequireNativeRefusesQEMUFallback exercises --require-native
// against a spec nativeKVMEligible always rejects (2 vCPUs - see
// TestNativeKVMEligible's "multiple vCPUs falls back" subtest), independent
// of whether this host actually has native KVM: without --require-native
// this would silently fall back to run-microvm.sh/QEMU (TestRunMicroVMNative
// already covers that default), but --require-native must instead fail
// closed and must never invoke the QEMU-based runner at all.
func TestRunMicroVMRequireNativeRefusesQEMUFallback(t *testing.T) {
	executed := false
	execute := func(string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		executed = true
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"run", "--backend=native", "--require-native", "--layout=/verified/layout", "--vcpus=2",
	}, &stdout, &stderr, execute)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if executed {
		t.Fatal("--require-native must never invoke the QEMU-based runner")
	}
	if !strings.Contains(stderr.String(), "--require-native was set") {
		t.Fatalf("stderr=%s", stderr.String())
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

// stubKubeVirtPlugin is a pluginClient double for dispatchKubeVirt's own
// routing logic (which capability, Call vs CallWithIdempotency, result
// formatting) - the same separation TestPluginHostDetectFreezeAndNotes
// already uses for detect/freeze/plan, so these tests do not need a real
// plugin subprocess. TestRunMicroVMKubeVirtCreateThroughRealPlugin below
// covers the real discover->verify->start->call path end to end instead.
type stubKubeVirtPlugin struct {
	capabilities []string
	calls        []string
	result       microvmapp.Result
	err          error
}

func (s *stubKubeVirtPlugin) Hello() plugin.HelloResult {
	return plugin.HelloResult{Name: "kubevirt", Capabilities: s.capabilities}
}
func (s *stubKubeVirtPlugin) HasCapability(capability string) bool {
	for _, declared := range s.capabilities {
		if declared == capability {
			return true
		}
	}
	return false
}
func (s *stubKubeVirtPlugin) Close() error { return nil }
func (s *stubKubeVirtPlugin) Call(_ context.Context, method string, params, result any) error {
	s.calls = append(s.calls, "call:"+method)
	if s.err != nil {
		return s.err
	}
	data, _ := json.Marshal(s.result)
	return json.Unmarshal(data, result)
}
func (s *stubKubeVirtPlugin) CallWithIdempotency(_ context.Context, operationID core.OperationID, method string, params, result any) error {
	s.calls = append(s.calls, "idempotent:"+method+":"+string(operationID))
	if s.err != nil {
		return s.err
	}
	data, _ := json.Marshal(s.result)
	return json.Unmarshal(data, result)
}

func allKubeVirtCapabilities() []string {
	return []string{
		api.CapabilityRuntimeCreate, api.CapabilityRuntimeStart, api.CapabilityRuntimeStop,
		api.CapabilityRuntimeRestart, api.CapabilityRuntimeStatus, api.CapabilityRuntimeLogs,
		api.CapabilityRuntimeDelete, api.CapabilityRuntimeRBAC,
	}
}

func TestDispatchKubeVirtRoutesMutatingActionsThroughIdempotency(t *testing.T) {
	stub := &stubKubeVirtPlugin{capabilities: allKubeVirtCapabilities()}
	host := &pluginHost{clients: []pluginClient{stub}}
	spec := microvm.Spec{Name: "demo", Namespace: "production"}

	for _, action := range []string{"start", "stop", "restart", "delete", "create", "rbac"} {
		var stdout, stderr bytes.Buffer
		if code := dispatchKubeVirt(context.Background(), host, action, spec, nil, false, &stdout, &stderr); code != 0 {
			t.Fatalf("%s code=%d stderr=%s", action, code, stderr.String())
		}
	}
	for _, action := range []string{"status", "logs"} {
		var stdout, stderr bytes.Buffer
		if code := dispatchKubeVirt(context.Background(), host, action, spec, nil, false, &stdout, &stderr); code != 0 {
			t.Fatalf("%s code=%d stderr=%s", action, code, stderr.String())
		}
	}
	if len(stub.calls) != 8 {
		t.Fatalf("calls=%v", stub.calls)
	}
	for i, action := range []string{"start", "stop", "restart", "delete", "create", "rbac"} {
		if !strings.HasPrefix(stub.calls[i], "idempotent:v1.runtime."+action+":") {
			t.Fatalf("action %s did not go through CallWithIdempotency: calls[%d]=%q", action, i, stub.calls[i])
		}
	}
	if stub.calls[6] != "call:v1.runtime.status" || stub.calls[7] != "call:v1.runtime.logs" {
		t.Fatalf("status/logs did not go through the read-only Call path: calls=%v", stub.calls)
	}
}

func TestDispatchKubeVirtSendsSpecFieldsAsParams(t *testing.T) {
	stub := &stubKubeVirtPlugin{capabilities: allKubeVirtCapabilities()}
	host := &pluginHost{clients: []pluginClient{stub}}
	spec := microvm.Spec{
		Name: "demo", Namespace: "production",
		Image: "registry.example/boot@sha256:" + strings.Repeat("b", 64),
	}
	var stdout, stderr bytes.Buffer
	if code := dispatchKubeVirt(context.Background(), host, "create", spec, repeatedFlag{"8080:80"}, true, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if len(stub.calls) != 1 {
		t.Fatalf("calls=%v", stub.calls)
	}
}

// The main module never renders a KubeVirt manifest or validates
// KubeVirt-specific fields itself - both live in the plugins/kubevirt
// module, dispatched by capability through internal/plugin, not driven
// as a hardcoded subprocess. This proves dispatchKubeVirt reflects the
// plugin's own response (manifest text, applied flag) back to the caller;
// manifest rendering and validation are covered by
// plugins/kubevirt/cmd/platform-factory-kubevirt's own tests.
func TestDispatchKubeVirtReflectsPluginResult(t *testing.T) {
	stub := &stubKubeVirtPlugin{
		capabilities: allKubeVirtCapabilities(),
		result:       microvmapp.Result{Manifest: `{"kind": "VirtualMachine"}`},
	}
	host := &pluginHost{clients: []pluginClient{stub}}
	spec := microvm.Spec{Name: "demo", Namespace: "default"}
	var stdout, stderr bytes.Buffer
	if code := dispatchKubeVirt(context.Background(), host, "create", spec, nil, false, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"kind": "VirtualMachine"`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestDispatchKubeVirtPropagatesPluginRejection(t *testing.T) {
	stub := &stubKubeVirtPlugin{capabilities: allKubeVirtCapabilities(), err: errors.New("plugin refused")}
	host := &pluginHost{clients: []pluginClient{stub}}
	spec := microvm.Spec{Name: "demo", Namespace: "default"}
	var stdout, stderr bytes.Buffer
	code := dispatchKubeVirt(context.Background(), host, "create", spec, nil, false, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "plugin refused") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunMicroVMKubeVirtFailsClosedWithoutAnInstalledPlugin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"create", "--backend=kubevirt", "--name=demo", "--namespace=default",
		"--image=registry.example/boot@sha256:" + strings.Repeat("b", 64),
	}, &stdout, &stderr, nil)
	if code == 0 {
		t.Fatal("kubevirt backend succeeded with no --plugin-dir configured")
	}
	if !strings.Contains(stderr.String(), "runtime.create") || !strings.Contains(stderr.String(), "--plugin-dir") {
		t.Fatalf("stderr does not explain the missing plugin: %s", stderr.String())
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

func TestRunMicroVMHelpShowsMicroVMUsageNotGlobalUsage(t *testing.T) {
	execute := func(string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("executor called for --help")
		return nil
	}
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}} {
		var stdout, stderr bytes.Buffer
		if code := runMicroVM(args, &stdout, &stderr, execute); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "platform-factory microvm <") {
			t.Fatalf("args=%v did not print microvm-specific usage: %s", args, stdout.String())
		}
		if strings.Contains(stdout.String(), "Common commands:") {
			t.Fatalf("args=%v printed the global top-level usage instead of microvm's own: %s", args, stdout.String())
		}
	}

	// The same must hold reached through the real top-level dispatcher,
	// which previously intercepted "microvm --help" before runMicroVM ever
	// saw it and printed the global usage instead.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"microvm", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "platform-factory microvm <") {
		t.Fatalf("run() did not print microvm-specific usage: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "Common commands:") {
		t.Fatalf("run() printed the global top-level usage instead of microvm's own: %s", stdout.String())
	}
}

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

func TestRunHelpAllPrintsFullUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"help", "--all"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Usage:", "Deprecated aliases", "platform-factory microvm <", "platform-factory plugin <"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help --all output missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "Common commands:") {
		t.Fatalf("help --all printed the short usage instead of the full one: %s", out)
	}
}

func TestRunDetectUsesLoadedLanguagePlugins(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "package-lock.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "requirements.lock"), []byte(""), 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"detect", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stdout.String(), `"kind": "node"`) || !strings.Contains(stdout.String(), "package-lock.json") {
		t.Fatalf("loaded node plugin did not classify its own marker: %s", stdout.String())
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

func TestRunInspectAndVerifyOCIArchive(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	_ = os.WriteFile(binary, []byte("payload"), 0o755)
	layoutDir := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{Binary: binary, Output: layoutDir}); err != nil {
		t.Fatal(err)
	}
	archiveName := filepath.Join(root, "layout.tar.gz")
	archive, err := os.Create(archiveName)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(archive)
	tw := tar.NewWriter(gz)
	if err := filepath.Walk(layoutDir, func(name string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || name == layoutDir {
			return walkErr
		}
		relative, _ := filepath.Rel(layoutDir, name)
		header, _ := tar.FileInfoHeader(info, "")
		header.Name = filepath.ToSlash(relative)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(name)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, file)
			closeErr := file.Close()
			return errors.Join(copyErr, closeErr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(tw.Close(), gz.Close(), archive.Close()); err != nil {
		t.Fatal(err)
	}
	archiveBytes, err := os.ReadFile(archiveName)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archiveBytes)
	for _, command := range []string{"inspect", "verify"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{command, "--archive-format", "oci-layout.tar.gz", "--sha256", "sha256:" + hex.EncodeToString(digest[:]), archiveName}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s code=%d stderr=%s", command, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), `"valid": true`) || !strings.Contains(stdout.String(), `"manifests": 1`) {
			t.Fatalf("%s output=%s", command, stdout.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"verify", "--archive-format", "tar", archiveName}, &stdout, &stderr); code != 2 {
		t.Fatalf("unsupported format code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"verify", "--archive-format", "oci-layout.tar.gz", "--sha256", strings.Repeat("0", 64), archiveName}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "SHA-256 mismatch") {
		t.Fatalf("digest mismatch code=%d stderr=%s", code, stderr.String())
	}
	for _, invalid := range []string{"short", strings.Repeat("A", 64)} {
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{"verify", "--archive-format", "oci-layout.tar.gz", "--sha256", invalid, archiveName}, &stdout, &stderr); code != 2 {
			t.Fatalf("invalid digest %q code=%d stderr=%s", invalid, code, stderr.String())
		}
	}
}

func TestRunVerifyDockerSaveArchive(t *testing.T) {
	root := t.TempDir()
	layoutName := buildPublishLayout(t, "example/service", "v1")
	archiveName := filepath.Join(root, "docker-save.tar")
	file, err := os.Create(archiveName)
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- dockersave.WriteDockerArchive(writer, layoutName, "example/service:v1") }()
	_, copyErr := io.Copy(file, reader)
	writeErr := <-done
	if err := errors.Join(copyErr, writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"verify", "--archive-format", "docker-save.tar", archiveName}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"images": 1`) || !strings.Contains(stdout.String(), `"example/service:v1"`) {
		t.Fatalf("output=%s", stdout.String())
	}
	if err := os.WriteFile(archiveName, []byte("not a tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"verify", "--archive-format", "docker-save.tar", archiveName}, &stdout, &stderr); code != 1 {
		t.Fatalf("malformed archive code=%d stderr=%s", code, stderr.String())
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

func TestRunBuildEnforcesCLIResourceBudgetAndLeavesNoLayout(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, bytes.Repeat([]byte("x"), 1<<20), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--max-wall-clock", "1ns", "--output", output, binary}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "budget exceeded") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("budget failure left output behind: %v", err)
	}
}

func TestRunBuildRejectsInvalidResourceBudgets(t *testing.T) {
	for _, args := range [][]string{
		{"--max-memory", "12MB", "app"},
		{"--max-memory", "-1", "app"},
		{"--max-wall-clock", "-1s", "app"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runBuild(context.Background(), args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "invalid resource budget") {
			t.Fatalf("args=%v code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
	for input, want := range map[string]int64{"0": 0, "512MiB": 512 << 20, "2GiB": 2 << 30, "4096": 4096} {
		got, err := buildapp.ParseByteLimit(input)
		if err != nil || got != want {
			t.Fatalf("buildapp.ParseByteLimit(%q)=%d,%v want=%d", input, got, err, want)
		}
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
	if code := run([]string{"build", "--dry-run", "--max-wall-clock", "30s", "--max-cpu", "10s", "--max-memory", "512MiB", "--output", output, binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"dry_run": true`, `"profile": "static"`, `"max_wall_clock": "30s"`, `"max_cpu": "10s"`, `"max_memory_bytes": 536870912`, `"valid": true`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %s: %s", want, stdout.String())
		}
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the layout: %v", err)
	}
}

func TestRunBuildDistRespectsExplicitOutput(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(root, "dist")
	explicitOutput := filepath.Join(root, "explicit-layout")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--dist", dist, "--output", explicitOutput, binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := layout.Verify(explicitOutput); err != nil {
		t.Fatalf("--output was not honored alongside --dist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dist, "oci-layout")); !os.IsNotExist(err) {
		t.Fatalf("--dist wrote oci-layout/ despite explicit --output: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dist, "sbom.json")); err != nil {
		t.Fatalf("dist/sbom.json still expected alongside explicit --output: %v", err)
	}
}

func TestRunBuildRebuildWritesDistSBOMAlongsideReproducibilityReport(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("reproducible dist payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(root, "dist")
	reports := filepath.Join(root, "reports")
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"build", "--rebuild=2", "--require-identical",
		"--dist", dist, "--reports", reports, binary,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}

	reproBytes, err := os.ReadFile(filepath.Join(reports, "reproducibility.json"))
	if err != nil {
		t.Fatalf("read reports/reproducibility.json: %v", err)
	}
	var repro map[string]any
	if err := json.Unmarshal(reproBytes, &repro); err != nil {
		t.Fatalf("decode reports/reproducibility.json: %v", err)
	}
	if repro["reproducible"] != true {
		t.Fatalf("reports/reproducibility.json reproducible=%v, want true", repro["reproducible"])
	}
	if _, err := os.Stat(filepath.Join(dist, "sbom.json")); err != nil {
		t.Fatalf("dist/sbom.json expected on the rebuild success path: %v", err)
	}
}

// TestRunBuildRebuildSurfacesADistSBOMWriteFailure covers
// runReproducibleBuild's WriteSBOMToDist error branch - the sibling
// success case is TestRunBuildRebuildWritesDistSBOMAlongsideReproducibilityReport.
// Pointing --dist at a path whose parent is a regular file (not a
// directory) makes buildapp.WriteSBOMToDist's own os.MkdirAll(distDir)
// fail deterministically, without needing a real permission-denied
// filesystem setup.
func TestRunBuildRebuildSurfacesADistSBOMWriteFailure(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("reproducible dist payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(blocker, "dist")
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"build", "--rebuild=2", "--require-identical", "--dist", dist, "--output", output, binary,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "write SBOM") {
		t.Fatalf("expected a write-SBOM error, stderr=%q", stderr.String())
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

// TestRunBuildRebuildSurfacesAnOutputParentCreationFailure covers
// runReproducibleBuild's own os.MkdirAll(filepath.Dir(output)) error
// branch: a read-only ancestor directory makes creating output's own
// parent directory fail with a permission error. Skipped when running
// as root (e.g. some CI/container setups), where permission bits on a
// directory the process itself owns are not enforced.
func TestRunBuildRebuildSurfacesAnOutputParentCreationFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	readonly := filepath.Join(root, "readonly")
	if err := os.Mkdir(readonly, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readonly, 0o755) })
	if err := os.Chmod(readonly, 0o555); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(readonly, "sub", "layout")
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "--rebuild=2", "--output", output, binary}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "create output parent") {
		t.Fatalf("expected a create-output-parent error, stderr=%q", stderr.String())
	}
}

// TestRunBuildRebuildSurfacesATemporaryDirectoryCreationFailure covers
// runReproducibleBuild's own os.MkdirTemp error branch, distinct from
// the MkdirAll failure above: output's parent directory already exists
// (so MkdirAll trivially succeeds without needing write access) but is
// itself read-only, so creating a new temporary directory inside it
// fails.
func TestRunBuildRebuildSurfacesATemporaryDirectoryCreationFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	readonly := filepath.Join(root, "readonly")
	if err := os.Mkdir(readonly, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readonly, 0o755) })
	if err := os.Chmod(readonly, 0o555); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(readonly, "layout")
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "--rebuild=2", "--output", output, binary}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "create temporary directory") {
		t.Fatalf("expected a create-temporary-directory error, stderr=%q", stderr.String())
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
	if code := emitRebuildResult(outcome, "json", "", &stdout, &stderr); code != 1 {
		t.Fatalf("require-identical divergence code=%d", code)
	}
	if !strings.Contains(stdout.String(), `"reproducible": false`) || !strings.Contains(stdout.String(), `"divergences"`) {
		t.Fatalf("json stdout=%s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	outcome.requireIdentical = false
	if code := emitRebuildResult(outcome, "text", "", &stdout, &stderr); code != 0 {
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

func TestRunBuildWritesDistAndReports(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(root, "dist")
	reports := filepath.Join(root, "reports")
	keys := filepath.Join(root, "keys")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--dist", dist, "--reports", reports, "--sign-key-dir", keys, binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if _, err := layout.Verify(filepath.Join(dist, "oci-layout")); err != nil {
		t.Fatalf("verify dist layout: %v", err)
	}
	for _, path := range []string{
		filepath.Join(dist, "sbom.json"), filepath.Join(dist, "provenance.json"),
		filepath.Join(dist, "attestations", "provenance.dsse.json"),
		filepath.Join(dist, "signatures", "subject.dsse.json"),
		filepath.Join(reports, "build.json"), filepath.Join(reports, "policy.json"), filepath.Join(reports, "metrics.json"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatalf("invalid JSON in %s: %v", path, err)
		}
	}
	buildReport, err := os.ReadFile(filepath.Join(reports, "build.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buildReport), filepath.Join(dist, "oci-layout")) || !strings.Contains(string(buildReport), `"valid": true`) {
		t.Fatalf("build report=%s", buildReport)
	}
	sbomReport, err := os.ReadFile(filepath.Join(dist, "sbom.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sbomReport), `"name":"/app/service"`) {
		t.Fatalf("sbom report=%s", sbomReport)
	}
	provenanceReport, err := os.ReadFile(filepath.Join(dist, "provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(provenanceReport), `"subject_digest": "sha256:`) {
		t.Fatalf("provenance report=%s", provenanceReport)
	}
	policyReport, err := os.ReadFile(filepath.Join(reports, "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(policyReport), `"allowed": true`) {
		t.Fatalf("policy report=%s", policyReport)
	}
	metricsReport, err := os.ReadFile(filepath.Join(reports, "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"api_version": "platform-factory.dev/metrics/v1"`, `"operation": "build"`, `"success": true`, `"platforms": 1`} {
		if !strings.Contains(string(metricsReport), want) {
			t.Fatalf("metrics report missing %s: %s", want, metricsReport)
		}
	}
	envelopeData, err := os.ReadFile(filepath.Join(dist, "signatures", "subject.dsse.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope attestation.Envelope
	if err := json.Unmarshal(envelopeData, &envelope); err != nil {
		t.Fatal(err)
	}
	store, err := signing.NewFileKeyStore(keys)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := store.PublicKey("release")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attestation.Verify(envelope, map[string]ed25519.PublicKey{envelope.Signatures[0].KeyID: publicKey}); err != nil {
		t.Fatalf("verify subject envelope: %v", err)
	}
	summary, err := os.ReadFile(filepath.Join(reports, "summary.txt"))
	if err != nil || !strings.Contains(string(summary), "Policy: allowed=true") {
		t.Fatalf("summary=%s err=%v", summary, err)
	}
}

func TestRunBuildRebuildWritesReproducibilityReport(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(root, "reports")
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"build", "--rebuild=2", "--output", filepath.Join(root, "layout"), "--reports", reports, binary,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(reports, "reproducibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Rebuilds     int  `json:"rebuilds"`
		Reproducible bool `json:"reproducible"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Rebuilds != 2 || !report.Reproducible {
		t.Fatalf("report=%s", data)
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

// TestRunBuildRejectsAFlagParseFailure covers runBuild's flags.Parse
// error branch (distinct from flag.ErrHelp and from every individual
// usage-validation check further down): a flag given a value its own
// type cannot parse fails inside flag.FlagSet.Parse itself.
func TestRunBuildRejectsAFlagParseFailure(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--max-cpu", "not-a-duration", binary}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunBuildMultiPlatformTextOutputPluralizes covers runBuild's text-
// format multi-platform summary line, including the "platforms" (not
// "platform") pluralization branch - TestBuildAndComposeTextOutput's own
// text-format case only ever builds one platform per invocation, and
// TestRunBuildMultiPlatformInOneCommand never asks for text output.
func TestRunBuildMultiPlatformTextOutputPluralizes(t *testing.T) {
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
	code := run([]string{
		"build", "--format", "text", "-o", output,
		"--platform", "linux/amd64=" + amd64,
		"--platform", "linux/arm64=" + arm64,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "(2 platforms)") {
		t.Fatalf("expected pluralized platform count, stdout=%s", stdout.String())
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
		if _, _, err := buildapp.Targets(test.platforms, test.positional, "linux", "amd64"); err == nil {
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

func TestRunMicroVMInspectLegacyDiskRequiresDiskFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{"inspect-legacy-disk"}, &stdout, &stderr, nil)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--disk is required") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestRunMicroVMInspectLegacyDiskWritesReportsAndNeverInvokesARunner(t *testing.T) {
	osDisk := filepath.Join(t.TempDir(), "os.raw")
	header := make([]byte, 4096)
	header[446] = 0x80 // active/bootable
	header[446+4] = 0x83
	header[510], header[511] = 0x55, 0xaa
	if err := os.WriteFile(osDisk, header, 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	execute := func(name string, args, environment []string, _ io.Reader, _, _ io.Writer) error {
		t.Fatalf("inspect-legacy-disk must never invoke a runner; name=%s args=%v", name, args)
		return nil
	}
	reportDir := filepath.Join(t.TempDir(), "reports")
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"inspect-legacy-disk", "--disk=" + osDisk, "--report-dir=" + reportDir,
	}, &stdout, &stderr, execute)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	jsonBytes, err := os.ReadFile(filepath.Join(reportDir, "discovery.json"))
	if err != nil {
		t.Fatalf("read discovery.json: %v", err)
	}
	var report vmdisk.DiscoveryReport
	if err := json.Unmarshal(jsonBytes, &report); err != nil {
		t.Fatalf("unmarshal discovery.json: %v", err)
	}
	if !report.BootDiskResolved || len(report.Disks) != 1 || report.Disks[0].Path != osDisk {
		t.Fatalf("report=%+v", report)
	}
	if report.Disks[0].SHA256 == "" || !strings.HasPrefix(report.Disks[0].SHA256, "sha256:") {
		t.Fatalf("SHA256=%q", report.Disks[0].SHA256)
	}
	textBytes, err := os.ReadFile(filepath.Join(reportDir, "discovery.txt"))
	if err != nil {
		t.Fatalf("read discovery.txt: %v", err)
	}
	if !strings.Contains(string(textBytes), osDisk) {
		t.Fatalf("discovery.txt missing disk path:\n%s", textBytes)
	}
	if !strings.Contains(stdout.String(), osDisk) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	compatibilityBytes, err := os.ReadFile(filepath.Join(reportDir, "compatibility.json"))
	if err != nil {
		t.Fatalf("read compatibility.json: %v", err)
	}
	var compatibility vmdisk.CompatibilityReport
	if err := json.Unmarshal(compatibilityBytes, &compatibility); err != nil {
		t.Fatalf("unmarshal compatibility.json: %v", err)
	}
	if compatibility.RecommendedMode != vmdisk.ModeUnsupported || !compatibility.DeploymentBlocked || compatibility.DiscoveryDigest == "" {
		t.Fatalf("compatibility=%+v", compatibility)
	}
	if text, err := os.ReadFile(filepath.Join(reportDir, "compatibility.txt")); err != nil || !strings.Contains(string(text), "Conditions preventing deployment") {
		t.Fatalf("compatibility.txt=%q err=%v", text, err)
	}
}

func TestRunMicroVMInspectLegacyDiskExplicitEncapsulationAndInvalidStrategy(t *testing.T) {
	osDisk := filepath.Join(t.TempDir(), "os.raw")
	header := make([]byte, 4096)
	header[446], header[450], header[510], header[511] = 0x80, 0x83, 0x55, 0xaa
	if err := os.WriteFile(osDisk, header, 0o644); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(t.TempDir(), "reports")
	var stdout, stderr bytes.Buffer
	if code := runMicroVM([]string{"inspect-legacy-disk", "--disk=" + osDisk, "--strategy=vm-encapsulation", "--report-dir=" + reportDir}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(reportDir, "compatibility.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report vmdisk.CompatibilityReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.RecommendedMode != vmdisk.ModeVMEncapsulation || report.DeploymentBlocked || report.AutomaticDecision {
		t.Fatalf("report=%+v", report)
	}

	invalidDir := filepath.Join(t.TempDir(), "must-not-exist")
	stdout.Reset()
	stderr.Reset()
	if code := runMicroVM([]string{"inspect-legacy-disk", "--disk=" + osDisk, "--strategy=magic", "--report-dir=" + invalidDir}, &stdout, &stderr, nil); code != 2 || !strings.Contains(stderr.String(), "unknown compatibility strategy") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(invalidDir); !os.IsNotExist(err) {
		t.Fatalf("invalid strategy wrote reports: %v", err)
	}
}

func TestRunMicroVMInspectLegacyDiskAmbiguousBootDiskStillWritesAReport(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first.raw")
	second := filepath.Join(t.TempDir(), "second.raw")
	for _, path := range []string{first, second} {
		header := make([]byte, 4096)
		header[510], header[511] = 0x55, 0xaa
		if err := os.WriteFile(path, header, 0o644); err != nil {
			t.Fatalf("write disk: %v", err)
		}
	}
	reportDir := filepath.Join(t.TempDir(), "reports")
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"inspect-legacy-disk", "--disk=" + first, "--disk=" + second, "--report-dir=" + reportDir,
	}, &stdout, &stderr, nil)
	// Unlike run-legacy-disk, an inconclusive boot-disk decision must not
	// fail the whole command - the report itself records the ambiguity.
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	jsonBytes, err := os.ReadFile(filepath.Join(reportDir, "discovery.json"))
	if err != nil {
		t.Fatalf("read discovery.json: %v", err)
	}
	var report vmdisk.DiscoveryReport
	if err := json.Unmarshal(jsonBytes, &report); err != nil {
		t.Fatalf("unmarshal discovery.json: %v", err)
	}
	if report.BootDiskResolved {
		t.Fatal("BootDiskResolved=true, want false for an ambiguous pair")
	}
	if len(report.Disks) != 2 {
		t.Fatalf("Disks=%+v", report.Disks)
	}
	foundAmbiguity := false
	for _, item := range report.HumanReviewItems {
		if strings.Contains(item, "--boot-disk") {
			foundAmbiguity = true
		}
	}
	if !foundAmbiguity {
		t.Fatalf("HumanReviewItems=%+v", report.HumanReviewItems)
	}
}

func TestRunMicroVMInspectLegacyDiskFailsClosedOnUnrecognizedFormat(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "not-a-disk.bin")
	if err := os.WriteFile(disk, bytes.Repeat([]byte{0x42}, 512), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := runMicroVM([]string{
		"inspect-legacy-disk", "--disk=" + disk, "--report-dir=" + filepath.Join(t.TempDir(), "reports"),
	}, &stdout, &stderr, nil)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unrecognized or unsupported disk image format") {
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
	// A bare `pf run` with no positional IMAGE/layout now falls back to
	// project mode (the same "just works" shorthand `pf launch` already
	// gives bare invocations) - with no project here either, it reports
	// the friendly `pf init` pointer rather than runContainer's old
	// generic usage error.
	if code := run([]string{"run"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "pf init") {
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
	if code := runContainer(context.Background(), []string{"--help"}, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("--help code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runContainer(context.Background(), []string{"--network=bridge", "--", "-badimage"}, &stdout, &stderr, execute); code != 2 ||
		!strings.Contains(stderr.String(), "invalid image reference") {
		t.Fatalf("dash-prefixed image code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunContainerRequiresATargetWithAClearError(t *testing.T) {
	execute := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("executor called with no target")
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runContainer(context.Background(), nil, &stdout, &stderr, execute); code != 2 ||
		!strings.Contains(stderr.String(), "an IMAGE or local OCI layout is required") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunContainerRejectsAPathShapedTargetThatDoesNotExist(t *testing.T) {
	execute := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("docker/podman must never be invoked for a nonexistent local-looking target")
		return nil
	}
	dir := t.TempDir()
	for _, target := range []string{
		filepath.Join(dir, "does-not-exist"), // absolute path
		"./relative-does-not-exist",          // "./" prefix
		".platform-factory/image",            // "." prefix without a slash - pf.yaml's own default build output
	} {
		var stdout, stderr bytes.Buffer
		if code := runContainer(context.Background(), []string{target}, &stdout, &stderr, execute); code != 1 ||
			!strings.Contains(stderr.String(), "looks like a local path") {
			t.Fatalf("target=%q code=%d stderr=%s", target, code, stderr.String())
		}
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
	if _, code, err := buildapp.Targets([]string{"linux/amd64"}, nil, "linux", "amd64"); err == nil || code != 2 {
		t.Fatalf("single platform without executable: code=%d err=%v", code, err)
	}
	if _, code, err := buildapp.Targets(
		[]string{"bogus/arch=a", "linux/amd64=b"}, nil, "linux", "amd64",
	); err == nil || code != 2 {
		t.Fatalf("invalid platform in multi-platform syntax: code=%d err=%v", code, err)
	}
	root := t.TempDir()
	script := filepath.Join(root, "app.py")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildapp.ResolveTarget(buildapp.Target{OS: "linux", Architecture: "amd64", Input: script}, buildapp.Settings{}); err == nil {
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

// TestRunBuildHelpFlagWithSingleDash covers runBuild's own flag.ErrHelp
// branch inside its flags.Parse error handling. containsHelpFlag only
// short-circuits on the exact literals "-h" and "--help"; a single-dash
// spelling of the long form ("-help") slips past that check but is still
// recognized by the flag package itself, which prints usage and returns
// flag.ErrHelp from Parse - a distinct path from both containsHelpFlag's
// early return and TestRunBuildRejectsAFlagParseFailure's genuine parse
// error.
func TestRunBuildHelpFlagWithSingleDash(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "-help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage of build") {
		t.Fatalf("expected flag-package usage output, stderr=%q", stderr.String())
	}
}

// TestRunBuildConfigSystemFilesAreAddedAsExtraFiles covers runBuild's
// loop that turns a strict config's system_files (CA bundle, timezone,
// locale archive) into extra files layered into the image - none of the
// other --config tests populate system_files, so that loop's append
// branch is otherwise never taken.
func TestRunBuildConfigSystemFilesAreAddedAsExtraFiles(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(root, "ca.crt")
	tz := filepath.Join(root, "localtime")
	locale := filepath.Join(root, "locale-archive")
	for _, file := range []string{ca, tz, locale} {
		if err := os.WriteFile(file, []byte("contents of "+file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "config.json")
	encoded, err := json.Marshal(map[string]any{
		"entrypoint": "/opt/service",
		"system_files": map[string]string{
			"ca_certificates": ca,
			"timezone":        tz,
			"locale_archive":  locale,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build", "--config", config, "--output", output, binary}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	report, err := layout.Verify(output)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Manifests != 1 {
		t.Fatalf("report=%+v", report)
	}
}

// TestRunBuildMultiPlatformSurfacesAnOutputParentCreationFailure covers
// runBuild's own os.MkdirAll(filepath.Dir(output)) error branch on the
// len(targets) > 1 path - distinct from runReproducibleBuild's identical
// check (TestRunBuildRebuildSurfacesAnOutputParentCreationFailure), which
// only ever drives a single target through --rebuild.
func TestRunBuildMultiPlatformSurfacesAnOutputParentCreationFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	root := t.TempDir()
	amd64 := filepath.Join(root, "service-amd64")
	arm64 := filepath.Join(root, "service-arm64")
	if err := os.WriteFile(amd64, []byte("amd64 executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(arm64, []byte("arm64 executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	readonly := filepath.Join(root, "readonly")
	if err := os.Mkdir(readonly, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readonly, 0o755) })
	if err := os.Chmod(readonly, 0o555); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(readonly, "sub", "multi")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"build", "-o", output,
		"--platform", "linux/amd64=" + amd64, "--platform", "linux/arm64=" + arm64,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "create output parent") {
		t.Fatalf("expected a create-output-parent error, stderr=%q", stderr.String())
	}
}

// TestRunBuildMultiPlatformSurfacesATemporaryDirectoryCreationFailure
// covers runBuild's own os.MkdirTemp error branch on the len(targets) > 1
// path, distinct from the MkdirAll failure above the same way
// TestRunBuildRebuildSurfacesATemporaryDirectoryCreationFailure is
// distinct from TestRunBuildRebuildSurfacesAnOutputParentCreationFailure:
// output's parent directory already exists (MkdirAll trivially succeeds)
// but is itself read-only, so creating the per-platform temporary
// directory inside it fails.
func TestRunBuildMultiPlatformSurfacesATemporaryDirectoryCreationFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	root := t.TempDir()
	amd64 := filepath.Join(root, "service-amd64")
	arm64 := filepath.Join(root, "service-arm64")
	if err := os.WriteFile(amd64, []byte("amd64 executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(arm64, []byte("arm64 executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	readonly := filepath.Join(root, "readonly")
	if err := os.Mkdir(readonly, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readonly, 0o755) })
	if err := os.Chmod(readonly, 0o555); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(readonly, "multi")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"build", "-o", output,
		"--platform", "linux/amd64=" + amd64, "--platform", "linux/arm64=" + arm64,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "create temporary directory") {
		t.Fatalf("expected a create-temporary-directory error, stderr=%q", stderr.String())
	}
}

// TestRunBuildMultiPlatformSurfacesASecondPlatformBuildFailure covers
// runBuild's per-platform buildapp.BuildImage error branch inside the
// multi-platform loop: the first --platform builds successfully into its
// own temporary directory before the second one's missing input surfaces
// an error, proving the loop (not just a first-iteration failure) is
// checked on every pass.
func TestRunBuildMultiPlatformSurfacesASecondPlatformBuildFailure(t *testing.T) {
	root := t.TempDir()
	amd64 := filepath.Join(root, "service-amd64")
	if err := os.WriteFile(amd64, []byte("amd64 executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	missingARM64 := filepath.Join(root, "service-arm64")
	output := filepath.Join(root, "multi")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"build", "-o", output,
		"--platform", "linux/amd64=" + amd64, "--platform", "linux/arm64=" + missingARM64,
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("missing second-platform input unexpectedly succeeded: %s", stdout.String())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("failed multi-platform build left an output: %v", err)
	}
}

// TestRunBuildMultiPlatformSurfacesAComposeFailure covers runBuild's
// layout.Compose error branch: both per-platform builds succeed into
// their own temporary directories, but something already sits at the
// final --output path, so layout.Compose itself refuses to install over
// it.
func TestRunBuildMultiPlatformSurfacesAComposeFailure(t *testing.T) {
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
	if err := os.WriteFile(output, []byte("preexisting, not a layout"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"build", "-o", output,
		"--platform", "linux/amd64=" + amd64, "--platform", "linux/arm64=" + arm64,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "compose platforms") {
		t.Fatalf("expected a compose-platforms error, stderr=%q", stderr.String())
	}
}

// TestRunBuildSurfacesABuildReportWriteFailure covers runBuild's
// atomicfile.WriteJSON error branch for reports/build.json: pre-creating
// a directory at that exact path makes the atomic rename that finishes
// the write collide with an existing entry, the same directory-collision
// trick TestRunBuildSurfacesAMetricsWriteFailure uses for metrics.json.
func TestRunBuildSurfacesABuildReportWriteFailure(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(root, "reports")
	if err := os.MkdirAll(filepath.Join(reports, "build.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "--reports", reports, "--output", output, binary}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "write build report") {
		t.Fatalf("expected a write-build-report error, stderr=%q", stderr.String())
	}
}

// TestRunBuildSurfacesADistSBOMWriteFailure covers runBuild's own (non-
// rebuild) buildapp.WriteSBOMToDist error branch - the sibling of
// TestRunBuildRebuildSurfacesADistSBOMWriteFailure, which only exercises
// that same helper from runReproducibleBuild. Pointing --dist at a path
// whose parent is a regular file makes WriteSBOMToDist's own
// os.MkdirAll(distDir) fail deterministically.
func TestRunBuildSurfacesADistSBOMWriteFailure(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(blocker, "dist")
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "--dist", dist, "--output", output, binary}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "write SBOM") {
		t.Fatalf("expected a write-SBOM error, stderr=%q", stderr.String())
	}
}

// TestRunBuildSurfacesAReleaseEvidenceWriteFailure covers runBuild's own
// buildapp.WriteBuildEvidence error branch. --dist is valid (so the
// preceding SBOM write succeeds) but --sign-key-dir points under a
// regular file, so signing.NewFileKeyStore's own directory creation
// fails once WriteBuildEvidence reaches the signing step.
func TestRunBuildSurfacesAReleaseEvidenceWriteFailure(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(root, "dist")
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	signKeyDir := filepath.Join(blocker, "keys")
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"build", "--dist", dist, "--sign-key-dir", signKeyDir, "--output", output, binary,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "write release evidence") {
		t.Fatalf("expected a write-release-evidence error, stderr=%q", stderr.String())
	}
}

// TestRunBuildSurfacesAMetricsWriteFailure covers runBuild's
// atomicfile.WriteJSON error branch for reports/metrics.json - reached
// only after layout.Verify(output) succeeds, so the failure has to come
// from the write itself rather than from an unusable output layout. Pre-
// creating a directory at that exact path makes the atomic rename that
// finishes the write collide with an existing entry.
func TestRunBuildSurfacesAMetricsWriteFailure(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(root, "reports")
	if err := os.MkdirAll(filepath.Join(reports, "metrics.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "--reports", reports, "--output", output, binary}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "write metrics") {
		t.Fatalf("expected a write-metrics error, stderr=%q", stderr.String())
	}
}

// TestRunBuildRebuildSurfacesANonNotExistStatError covers
// runReproducibleBuild's os.Stat(output) branch for an error other than
// "not exist" - distinct from TestRunBuildRebuildTextAndExistingOutput's
// "output already exists" case. Pointing output through a path component
// that is a regular file (not a directory) makes the stat fail with
// ENOTDIR, which errors.Is does not treat as os.ErrNotExist.
func TestRunBuildRebuildSurfacesANonNotExistStatError(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(blocker, "sub", "layout")
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "--rebuild=2", "--output", output, binary}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "stat output") {
		t.Fatalf("expected a stat-output error, stderr=%q", stderr.String())
	}
}

// TestRunDispatchesInitToRunInit covers run()'s "init" dispatch line
// itself (main.go's `if args[0] == "init" { return runInit(...) }`).
// runInit's own behavior is already covered thoroughly elsewhere
// (init_test.go, init_e2e_test.go, init_ux_test.go) via direct calls to
// runInit; no existing test drives it through the top-level run()
// entry point. A single invalid --engine value reaches one of runInit's
// earliest validation branches, so this stays fast and side-effect-free.
func TestRunDispatchesInitToRunInit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--engine=bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--engine must be docker or podman") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

// TestRunDispatchesImportToRunImport covers run()'s "import" dispatch
// line, the sibling of TestRunDispatchesInitToRunInit above - runImport
// itself is already covered directly in import_test.go. A bare `import`
// with no flags fails its own usage check immediately.
func TestRunDispatchesImportToRunImport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"import"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "usage: platform-factory import") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

// TestRunDispatchesLogsAndEventsToRunProjectObservation covers run()'s
// "logs"/"events" dispatch line (both command names share one branch).
// With no deployed project in a fresh directory, runProjectObservation
// fails fast on its own project.Discover call before touching any
// container runtime.
func TestRunDispatchesLogsAndEventsToRunProjectObservation(t *testing.T) {
	oldDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDirectory) })
	for _, command := range []string{"logs", "events"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{command}, &stdout, &stderr); code != 1 {
			t.Fatalf("%s code=%d stderr=%s", command, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "no deployed project") {
			t.Fatalf("%s stderr=%q", command, stderr.String())
		}
	}
}

// TestRunBuildDispatchesToProjectModeWhenAProjectIsDiscovered covers
// run()'s build-dispatch success branch: a bare `pf build` (no
// EXECUTABLE argument) with a real project config in the current
// directory takes the project.Discover(".", "") == nil path and hands
// off to runProjectContext, instead of falling through to the low-level
// runBuild(single executable) path every other build test uses. A
// "compiled" project with no build_command packages its existing
// artifact directly, so this needs no real toolchain or container
// runtime.
func TestRunBuildDispatchesToProjectModeWhenAProjectIsDiscovered(t *testing.T) {
	oldDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".config_image.yaml"), []byte("language: compiled\nartifact: app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldDirectory) })
	var stdout, stderr bytes.Buffer
	if code := run([]string{"build"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"valid": true`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if _, err := layout.Verify(filepath.Join(root, ".platform-factory", "image")); err != nil {
		t.Fatalf("project-mode build did not produce a valid layout: %v", err)
	}
}

// TestRunDetectSurfacesAmbiguousLanguagePlugins covers detect's
// Ambiguous-without---accept-ambiguous branch. detect.Path's own
// built-in classifier never sets Ambiguous; only the loaded-language-
// plugin fallback can (main.go's detectionFromPlugins), and only when
// two or more loaded plugins match the same directory -
// TestRunDetectUsesLoadedLanguagePlugins only ever gives one plugin a
// matching marker. A directory carrying both go.mod and package.json
// matches the go and node plugins simultaneously.
func TestRunDetectSurfacesAmbiguousLanguagePlugins(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"detect", root}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"kind": "ambiguous"`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "ambiguous detection requires --accept-ambiguous") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

// TestRunDetectSurfacesLanguagePluginInspectionFailure covers detect's
// langplugin.InspectLoaded error branch. Pointing
// PLATFORM_FACTORY_LANG_PLUGIN_DIR (t.Setenv, restored automatically)
// at a directory containing a same-prefix file that isn't executable
// makes List() pick it up but RunInspection's exec fail, the same
// env-var override TestMain itself and provisionruntime_test.go/
// plugin_test.go already use to point at a controlled plugin directory.
func TestRunDetectSurfacesLanguagePluginInspectionFailure(t *testing.T) {
	root := t.TempDir()
	pluginDir := t.TempDir()
	t.Setenv("PLATFORM_FACTORY_LANG_PLUGIN_DIR", pluginDir)
	broken := filepath.Join(pluginDir, "platform-factory-lang-broken")
	if err := os.WriteFile(broken, []byte("not an executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"detect", root}, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "inspect language plugins") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

// TestRunInspectSurfacesAGenuineFlagParseFailure covers runInspect's
// flags.Parse error branch for an error other than flag.ErrHelp -
// distinct from TestRunInspectHandlesHelpAndUsageDirectly's --help case.
// "--format" given no value fails inside flag.FlagSet.Parse itself.
func TestRunInspectSurfacesAGenuineFlagParseFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runInspect("inspect", []string{"--format"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunInspectRejectsSHA256WithoutArchiveFormat covers runInspect's
// own --sha256-without---archive-format validation branch.
func TestRunInspectRejectsSHA256WithoutArchiveFormat(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runInspect("inspect", []string{"--sha256", "abcd", "somelayout"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--sha256 requires --archive-format") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

// TestRunInspectSurfacesAnArchiveOpenFailure covers runInspect's
// os.Open(archive) error branch: a nonexistent archive path, past every
// flag-validation check, fails at the open itself.
func TestRunInspectSurfacesAnArchiveOpenFailure(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.tar.gz")
	var stdout, stderr bytes.Buffer
	code := runInspect("inspect", []string{"--archive-format", "oci-layout.tar.gz", missing}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "open archive") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

// TestRunInspectArchiveVerificationTextFormat covers runInspect's
// --format text output for a valid archive verification -
// TestRunInspectAndVerifyOCIArchive only ever asks for the default json
// format.
func TestRunInspectArchiveVerificationTextFormat(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	layoutDir := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{Binary: binary, Output: layoutDir}); err != nil {
		t.Fatal(err)
	}
	archiveName := filepath.Join(root, "layout.tar.gz")
	archive, err := os.Create(archiveName)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(archive)
	tw := tar.NewWriter(gz)
	if err := filepath.Walk(layoutDir, func(name string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || name == layoutDir {
			return walkErr
		}
		relative, _ := filepath.Rel(layoutDir, name)
		header, _ := tar.FileInfoHeader(info, "")
		header.Name = filepath.ToSlash(relative)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(name)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, file)
			closeErr := file.Close()
			return errors.Join(copyErr, closeErr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(tw.Close(), gz.Close(), archive.Close()); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runInspect("inspect", []string{"--format", "text", "--archive-format", "oci-layout.tar.gz", archiveName}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "valid: oci-layout.tar.gz archive sha256:") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

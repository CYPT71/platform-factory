package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestMain is required by MaybeApplyPluginSandboxHelper's documented
// contract: this test binary itself calls Start, so it must call the hook
// exactly as any other consumer's main() would, or the process Start
// re-execs on the sandboxed path would run the test suite recursively
// instead of the intended plugin target.
func TestMain(m *testing.M) {
	MaybeApplyPluginSandboxHelper()
	if os.Getenv("PLATFORM_FACTORY_TEST_EXIT_AFTER_HELLO") == "1" {
		raw, err := ReadMessage(bufio.NewReader(os.Stdin))
		if err != nil {
			os.Exit(2)
		}
		var request Request
		if json.Unmarshal(raw, &request) != nil {
			os.Exit(2)
		}
		result, _ := json.Marshal(HelloResult{APIVersion: ProtocolVersion, Name: "dying-plugin", Version: "1.0.0", Capabilities: []string{"detect"}})
		if WriteMessage(os.Stdout, Response{ID: request.ID, Result: result}) != nil {
			os.Exit(2)
		}
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRegistryAvailabilityTracksSpontaneousPluginExit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client, err := StartAllowingUnsandboxed(context.Background(), executable, nil, []string{"PLATFORM_FACTORY_TEST_EXIT_AFTER_HELLO=1"})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	manifest := Manifest{Name: "dying-plugin", Version: "1.0.0", Capabilities: []string{"detect"}}
	registry.registerDiscovered(manifest)
	if err := registry.publishAvailable(manifest, client); err != nil {
		t.Fatal(err)
	}
	if !registry.HasCapability("detect") {
		t.Fatal("live verified plugin was unavailable")
	}
	deadline := time.Now().Add(3 * time.Second)
	for registry.HasCapability("detect") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if registry.HasCapability("detect") {
		t.Fatal("plugin remained available after spontaneous subprocess exit")
	}
	if !registry.DeclaredHasCapability("detect") {
		t.Fatal("runtime exit erased diagnostic declaration evidence")
	}
}

// demoPluginPath builds the real cmd/platform-factory-plugin-demo binary once per
// test run and returns its path, so every test exercising a genuine
// subprocess reuses the same build instead of paying for it repeatedly.
var demoPluginPath = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "platform-factory-plugin-demo-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "platform-factory-plugin-demo")
	cmd := exec.Command("go", "build", "-o", binary, "github.com/CYPT71/secure-oci-base/cmd/platform-factory-plugin-demo")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build demo plugin: %w: %s", err, output)
	}
	return binary, nil
})

func TestClientRunsRealDemoPluginSubprocess(t *testing.T) {
	binary, err := demoPluginPath()
	if err != nil {
		t.Fatalf("build demo plugin: %v", err)
	}

	client, err := StartAllowingUnsandboxed(context.Background(), binary, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	if hello := client.Hello(); hello.Name != "platform-factory-plugin-demo" || hello.APIVersion != ProtocolVersion {
		t.Fatalf("hello=%+v", hello)
	}
	if !client.HasCapability("detect") {
		t.Fatalf("capabilities=%v", client.Hello().Capabilities)
	}

	var result map[string]any
	if err := client.Call(context.Background(), "v1.detect", map[string]string{"path": "."}, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	if _, ok := result["kind"]; !ok {
		t.Fatalf("result=%v", result)
	}
}

// TestClientFallsBackWhenSandboxUnavailable proves Start still launches a
// working plugin when wrapping the command for the namespace sandbox
// fails, as it does on hosts that refuse unprivileged namespace creation.
// That real kernel-level refusal is not something a portable test can
// force, so this substitutes a stub for the deliberately unexported
// pluginSandboxWrapper var to exercise the exact fallback branch in Start.
func TestClientRefusesWhenSandboxUnavailableUnlessPolicyAllowsDegradation(t *testing.T) {
	binary, err := demoPluginPath()
	if err != nil {
		t.Fatalf("build demo plugin: %v", err)
	}
	original := pluginSandboxWrapper
	pluginSandboxWrapper = func(*exec.Cmd) error { return errors.New("sandbox unavailable (stub)") }
	defer func() { pluginSandboxWrapper = original }()

	if client, err := Start(context.Background(), binary, nil, nil); err == nil {
		_ = client.Close()
		t.Fatal("required sandbox failure launched plugin")
	}
	client, err := StartAllowingUnsandboxed(context.Background(), binary, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	if hello := client.Hello(); hello.Name != "platform-factory-plugin-demo" {
		t.Fatalf("hello=%+v", hello)
	}
	var result map[string]any
	if err := client.Call(context.Background(), "v1.detect", map[string]string{"path": "."}, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
}

func TestStartReportsSandboxedAndFallbackExecFailures(t *testing.T) {
	original := pluginSandboxWrapper
	pluginSandboxWrapper = func(*exec.Cmd) error { return nil }
	defer func() { pluginSandboxWrapper = original }()

	missing := filepath.Join(t.TempDir(), "missing-plugin")
	if client, err := Start(context.Background(), missing, nil, nil); err == nil {
		_ = client.Close()
		t.Fatal("missing sandboxed executable started")
	}
	if client, err := StartAllowingUnsandboxed(context.Background(), missing, nil, nil); err == nil {
		_ = client.Close()
		t.Fatal("missing fallback executable started")
	}
}

func TestClientRunsRealDemoPluginRejectsUnknownPath(t *testing.T) {
	binary, err := demoPluginPath()
	if err != nil {
		t.Fatalf("build demo plugin: %v", err)
	}

	client, err := StartAllowingUnsandboxed(context.Background(), binary, nil, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer client.Close()

	err = client.Call(context.Background(), "v1.detect", map[string]string{"path": "/does/not/exist"}, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err=%v (%T)", err, err)
	}
}

package hooks

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHookProcessHelper(t *testing.T) {
	if os.Getenv("PF_HOOK_HELPER") != "1" {
		return
	}
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	result := struct {
		Event  json.RawMessage `json:"event"`
		Secret string          `json:"secret"`
		Active string          `json:"active"`
	}{payload, os.Getenv("PF_HOOK_SECRET"), os.Getenv(ActiveEnv)}
	out, _ := json.Marshal(result)
	if err := os.WriteFile(os.Args[len(os.Args)-1], out, 0600); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
func TestDispatchPassesOnlyAllowlistedEnvironmentAndStructuredEvent(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "event.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PF_HOOK_HELPER", "1")
	t.Setenv("PF_HOOK_SECRET", "do-not-inherit")
	cfg := Config{Version: 1, Hooks: map[string][]Action{"command.failed": {{Command: []string{executable, "-test.run=^TestHookProcessHelper$", "--", output}, TimeoutSeconds: 10, PassEnv: []string{"PF_HOOK_HELPER"}}}}}
	event := NewEvent("command.failed", "build", root, 1, time.Second)
	if err := Dispatch(context.Background(), cfg, event); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "do-not-inherit") || !strings.Contains(string(raw), `"active":"1"`) || !strings.Contains(string(raw), event.ID) {
		t.Fatalf("unsafe payload: %s", raw)
	}
}
func TestConfigRejectsImplicitCommandsAndWritableFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "hooks.json")
	tests := []string{
		`{"version":2,"hooks":{}}`,
		`{"version":1,"unexpected":true,"hooks":{}}`,
		`{"version":1,"hooks":{"untrusted.event":[]}}`,
		`{"version":1,"hooks":{"command.failed":[{"command":["relative-command"],"timeout_seconds":1}]}}`,
		`{"version":1,"hooks":{"command.failed":[{"command":["/bin/false"],"timeout_seconds":0}]}}`,
		`{"version":1,"hooks":{"command.failed":[{"command":["/bin/false"],"timeout_seconds":1,"pass_env":["PLATFORM_FACTORY_HOOKS_ACTIVE"]}]}}`,
		`{"version":1,"hooks":{}} {}`,
	}
	for _, raw := range tests {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := Load("relative.json"); err == nil {
		t.Fatal("relative config allowed")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"hooks":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0666); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatal("world writable config allowed")
		}
	}
}
func TestTimeoutAndUnknownEvents(t *testing.T) {
	if err := Dispatch(context.Background(), Config{}, Event{Kind: "other"}); err == nil {
		t.Fatal("unknown event allowed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := Config{Version: 1, Hooks: map[string][]Action{"command.failed": {{Command: []string{"/bin/sleep", "20"}, TimeoutSeconds: 1}}}}
	if err := Dispatch(ctx, cfg, NewEvent("command.failed", "build", t.TempDir(), 1, 0)); err == nil {
		t.Fatal("cancelled hook succeeded")
	}
}

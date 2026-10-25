package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLifecycleHooksPreserveOutputStatusAndRecursionGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix fixture executable")
	}
	path := filepath.Join(t.TempDir(), "hooks.json")
	raw := `{"version":1,"hooks":{"command.started":[{"command":["/usr/bin/false"],"timeout_seconds":1}],"command.succeeded":[{"command":["/usr/bin/false"],"timeout_seconds":1}],"command.failed":[{"command":["/usr/bin/false"],"timeout_seconds":1}]}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLATFORM_FACTORY_HOOKS_CONFIG", path)
	t.Setenv("PLATFORM_FACTORY_HOOKS_ACTIVE", "")
	var out, diagnostics bytes.Buffer
	if code := run([]string{"version"}, &out, &diagnostics); code != 0 || !strings.HasPrefix(out.String(), "platform-factory ") {
		t.Fatalf("callback changed CLI success: %d %s", code, &out)
	}
	if !strings.Contains(diagnostics.String(), "exit status preserved") {
		t.Fatal("missing callback failure diagnostic")
	}
	out.Reset()
	diagnostics.Reset()
	if code := run([]string{"extensions", "unknown"}, &out, &diagnostics); code != 2 {
		t.Fatalf("callback changed original error: %d", code)
	}
	t.Setenv("PLATFORM_FACTORY_HOOKS_ACTIVE", "1")
	diagnostics.Reset()
	if code := run([]string{"version"}, &out, &diagnostics); code != 0 || diagnostics.Len() != 0 {
		t.Fatal("recursive hooks executed")
	}
	t.Setenv("PLATFORM_FACTORY_HOOKS_ACTIVE", "")
	t.Setenv("PLATFORM_FACTORY_HOOKS_CONFIG", filepath.Join(t.TempDir(), "missing"))
	if code := run([]string{"version"}, &out, &diagnostics); code != 2 {
		t.Fatal("bad configuration accepted")
	}
}
func TestHooksCLIValidationAndEmit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"hooks":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	for _, args := range [][]string{{}, {"--help"}, {"validate", "--config", path}} {
		if code := runHooks(args, &out, &diagnostics); code != 0 {
			t.Fatalf("%v=%d %s", args, code, &diagnostics)
		}
	}
	out.Reset()
	if code := runHooks([]string{"emit", "--config", path, "--event", "command.failed", "--command", "build"}, &out, &diagnostics); code != 0 {
		t.Fatalf("emit=%d %s", code, &diagnostics)
	}
	var event map[string]any
	if err := json.Unmarshal(out.Bytes(), &event); err != nil || event["kind"] != "command.failed" {
		t.Fatalf("event=%s %v", &out, err)
	}
	for _, args := range [][]string{{"validate"}, {"validate", "--config", "missing"}, {"unknown", "--config", path}, {"emit", "--config", path, "--event", "untrusted"}} {
		if code := runHooks(args, &out, &diagnostics); code == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
}

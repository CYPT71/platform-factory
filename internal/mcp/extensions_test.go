package mcp

import (
	"context"
	"encoding/json"
	"github.com/CYPT71/platform-factory/internal/extensions"
	"github.com/CYPT71/platform-factory/internal/hooks"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRoutedExtensionToolsCoverFiftyContractsWithoutFiftyToolDescriptors(t *testing.T) {
	s := NewServer("test", "test")
	registerExtensions(s)
	if len(s.tools.list()) != 2 {
		t.Fatal("tool list grew with profile count")
	}
	catalog, _ := s.tools.get("pf_extension_catalog")
	raw, err := catalog.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 50 {
		t.Fatalf("catalog %d: %v", len(rows), err)
	}
	check, _ := s.tools.get("pf_extension_check")
	for _, p := range extensions.Catalog() {
		contract, _ := json.Marshal(map[string]any{"name": p.Name})
		text, err := check.Handler(context.Background(), contract)
		if err != nil || !strings.Contains(text, `"example"`) {
			t.Fatalf("missing contract %s: %v", p.Name, err)
		}
		for _, sample := range []struct {
			doc    map[string]any
			passed bool
		}{{p.Example, true}, {p.RejectedExample, false}} {
			args, _ := json.Marshal(map[string]any{"name": p.Name, "document": sample.doc})
			text, err := check.Handler(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			var result extensions.Report
			if err := json.Unmarshal([]byte(text), &result); err != nil || result.Passed != sample.passed {
				t.Fatalf("wrong MCP result %s: %v", text, err)
			}
		}
	}
	for _, raw := range []string{`{`, `{} {}`, `{"name":"unknown"}`, `{"name":"ai-token-budget","document":null}`, `{"name":"ai-token-budget","extra":1}`, strings.Repeat("x", extensions.MaxInputBytes+1)} {
		if _, err := check.Handler(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
func TestMCPToolHooksKeepOriginalResultAndNeverArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix callback fixture")
	}
	root := t.TempDir()
	path := filepath.Join(root, "hooks.json")
	raw := `{"version":1,"hooks":{"tool.failed":[{"command":["/usr/bin/false"],"timeout_seconds":1}]}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(hooks.ConfigEnv, path)
	t.Setenv(hooks.ActiveEnv, "")
	ctx, err := toolHookContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var logOutput strings.Builder
	logger := log.New(&logOutput, "", 0)
	s := newTestServer()
	request := request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"boom","arguments":{"token":"do-not-copy"}}`)}
	result := s.dispatch(ctx, logger, request)
	if result == nil || result.Error != nil || !strings.Contains(logOutput.String(), "tool hook failed") {
		t.Fatalf("callback changed tool result: %+v logs=%s", result, logOutput.String())
	}
	if strings.Contains(logOutput.String(), "do-not-copy") {
		t.Fatal("tool arguments leaked")
	}
	t.Setenv(hooks.ActiveEnv, "1")
	if active, err := toolHookContext(context.Background(), root); err != nil || active.Value(toolHookKey{}) != nil {
		t.Fatal("recursive hooks enabled")
	}
	t.Setenv(hooks.ActiveEnv, "")
	t.Setenv(hooks.ConfigEnv, filepath.Join(root, "missing"))
	if _, err := toolHookContext(context.Background(), root); err == nil {
		t.Fatal("bad hooks accepted")
	}
}

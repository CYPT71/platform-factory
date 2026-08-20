package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRepo builds a minimal repo with a real go.mod, a copy of
// sdk/plugin (needed for a generated plugin to actually build), and one
// scaffolded plugin - the same shape internal/mcp/plugins' own tests
// use, reused here so ModifyPlugin exercises a real
// plugins.InspectPlugin/Validate round trip, not a mock.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module github.com/CYPT71/platform-factory\n\ngo 1.25.12\n")
	// plugins.Create's own addPluginToGoWork appends every newly
	// scaffolded plugin's module path into this file's "use (...)"
	// block, so a minimal one must exist here too - see
	// internal/mcp/plugins/plugins_test.go's fixtureRepo for the same
	// fix, needed for the identical reason.
	write("go.work", "go 1.25.12\n\nuse (\n\t.\n)\n")
	write(".github/workflows/ci-security.yml", "steps:\n  - run: |\n      find cmd internal -name '*.go' -type f \\\n        -exec grep -nE 'os/exec|exec\\.Command' {} + || true\n")

	sdkPluginDir := findRealSDKPluginDir(t)
	entries, err := os.ReadDir(sdkPluginDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sdkPluginDir, name))
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join("sdk/plugin", name), string(data))
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "fixture@example.com")
	run("config", "user.name", "Fixture")
	run("config", "commit.gpgsign", "false")
	run("add", "-A")
	run("commit", "-m", "initial")
	return dir
}

func findRealSDKPluginDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "sdk", "plugin")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate sdk/plugin by walking up from the test's working directory")
		}
		dir = parent
	}
}

func createFixturePlugin(t *testing.T, repoRoot, name string) {
	t.Helper()
	full := filepath.Join(repoRoot, "plugins", name, "cmd", "platform-factory-"+name, "main.go")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"api_version":"platform-factory.dev/plugin-manifest/v1","name":"` + name +
		`","version":"0.1.0","family":"capability","capabilities":["detect"],"platforms":["linux/amd64"],` +
		`"executable":"platform-factory-` + name + `","digest":"sha256:` + strings.Repeat("0", 64) + `"}`
	if err := os.WriteFile(filepath.Join(repoRoot, "plugins", name, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "plugins", name, "go.mod"),
		[]byte("module github.com/CYPT71/platform-factory/plugins/"+name+"\n\ngo 1.25.12\n\nrequire github.com/CYPT71/platform-factory v0.0.2\n\nreplace github.com/CYPT71/platform-factory => ../..\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// fixtureRepo's own go.work only lists "." by default; a plugin
	// module built directly here (rather than through plugins.Create,
	// which registers its own scaffolds automatically via
	// addPluginToGoWork) must be added the same way, or go's workspace
	// mode resolves plugins/<name> as orphaned from the workspace and
	// `go build`/`go vet` inside it behaves differently than it would
	// standalone.
	workPath := filepath.Join(repoRoot, "go.work")
	data, err := os.ReadFile(workPath)
	if err != nil {
		t.Fatal(err)
	}
	entry := "./plugins/" + name
	if !strings.Contains(string(data), entry) {
		updated := strings.Replace(string(data), "\t.\n)", "\t.\n\t"+entry+"\n)", 1)
		if updated == string(data) {
			t.Fatalf("could not register %s in go.work's use block", entry)
		}
		if err := os.WriteFile(workPath, []byte(updated), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeClient returns a Client whose Complete() replies, one per call,
// from replies in order - the same fake-transport-driven test pattern
// as anthropic_test.go, layered here to drive the multi-call
// orchestration loops in orchestrate.go/implement.go.
func fakeClient(t *testing.T, replies []string) *Client {
	t.Helper()
	call := 0
	return &Client{
		APIKey: "test", Model: "test", BaseURL: apiURL,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) *http.Response {
			if call >= len(replies) {
				t.Fatalf("unexpected extra Complete() call (only %d replies configured)", len(replies))
			}
			text := replies[call]
			call++
			payload, _ := json.Marshal(messagesResponse{
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{{Type: "text", Text: text}},
			})
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload))}
		})},
	}
}

func TestParseEditsAcceptsAFencedJSONArray(t *testing.T) {
	edits, err := parseEdits("```json\n[{\"path\":\"a.go\",\"content\":\"package a\"}]\n```")
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].Path != "a.go" {
		t.Fatalf("edits=%v", edits)
	}
}

func TestParseEditsRejectsNonArrayReplies(t *testing.T) {
	if _, err := parseEdits("I think we should add a function."); err == nil {
		t.Fatal("expected an error for a non-JSON reply")
	}
}

func TestModifyPluginSucceedsOnFirstValidAttempt(t *testing.T) {
	dir := fixtureRepo(t)
	createFixturePlugin(t, dir, "widget")

	edit := `[{"path":"plugins/widget/README.md","content":"# widget\n\nUpdated.\n"}]`
	client := fakeClient(t, []string{edit})

	result, err := ModifyPlugin(context.Background(), client, dir, ModifyPluginRequest{
		Plugin: "widget", Request: "add a README",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Iterations != 1 {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "plugins", "widget", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Updated") {
		t.Fatalf("content=%q", data)
	}
}

func TestModifyPluginRejectsAnEditOutsideThePluginDirectory(t *testing.T) {
	dir := fixtureRepo(t)
	createFixturePlugin(t, dir, "widget")

	escape := `[{"path":"plugins/other/evil.go","content":"package other"}]`
	retry := `[{"path":"plugins/widget/README.md","content":"# widget\n"}]`
	client := fakeClient(t, []string{escape, retry})

	result, err := ModifyPlugin(context.Background(), client, dir, ModifyPluginRequest{
		Plugin: "widget", Request: "do something",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Iterations != 2 {
		t.Fatalf("result=%+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins", "other", "evil.go")); err == nil {
		t.Fatal("the out-of-scope edit must not have been written")
	}
}

func TestModifyPluginGivesUpAfterMaxIterations(t *testing.T) {
	dir := fixtureRepo(t)
	createFixturePlugin(t, dir, "widget")

	bad := "not json at all"
	client := fakeClient(t, []string{bad, bad, bad})

	_, err := ModifyPlugin(context.Background(), client, dir, ModifyPluginRequest{
		Plugin: "widget", Request: "do something",
	})
	if err == nil {
		t.Fatal("expected an error after exhausting maxIterations")
	}
}

func TestModifyPluginRequiresANonEmptyRequest(t *testing.T) {
	dir := fixtureRepo(t)
	createFixturePlugin(t, dir, "widget")
	client := fakeClient(t, nil)
	if _, err := ModifyPlugin(context.Background(), client, dir, ModifyPluginRequest{Plugin: "widget"}); err == nil {
		t.Fatal("expected an error for an empty request")
	}
}

// TestModifyPluginPropagatesAnInspectPluginFailure covers ModifyPlugin's
// own plugins.InspectPlugin error branch - every other ModifyPlugin
// test in this file targets a plugin that createFixturePlugin actually
// scaffolded.
func TestModifyPluginPropagatesAnInspectPluginFailure(t *testing.T) {
	dir := fixtureRepo(t)
	client := fakeClient(t, nil)
	if _, err := ModifyPlugin(context.Background(), client, dir, ModifyPluginRequest{
		Plugin: "does-not-exist", Request: "do something",
	}); err == nil {
		t.Fatal("expected plugins.InspectPlugin's error to propagate")
	}
}

// TestModifyPluginPropagatesAClientCompleteFailure covers ModifyPlugin's
// own client.Complete error branch - fakeClient's usual roundTripFunc
// always returns HTTP 200; this one returns a non-200 status with an
// API error body instead, the same shape TestCompleteSurfacesAnAPIError
// (anthropic_test.go) uses to exercise Complete() itself, layered here
// to prove ModifyPlugin propagates it rather than retrying past it.
func TestModifyPluginPropagatesAClientCompleteFailure(t *testing.T) {
	dir := fixtureRepo(t)
	createFixturePlugin(t, dir, "widget")
	client := &Client{
		APIKey: "test", Model: "test", BaseURL: apiURL,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) *http.Response {
			payload, _ := json.Marshal(messagesResponse{
				Error: &struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				}{Type: "overloaded_error", Message: "the API is temporarily overloaded"},
			})
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(bytes.NewReader(payload))}
		})},
	}
	_, err := ModifyPlugin(context.Background(), client, dir, ModifyPluginRequest{
		Plugin: "widget", Request: "do something",
	})
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err=%v", err)
	}
}

func TestPatchCoreRequiresAllowedPaths(t *testing.T) {
	dir := fixtureRepo(t)
	client := fakeClient(t, nil)
	_, err := PatchCore(context.Background(), client, dir, PatchCoreRequest{Request: "x"})
	if err == nil {
		t.Fatal("expected an error without allowed_paths")
	}
}

func TestPatchCoreRejectsAnEditOutsideAllowedPaths(t *testing.T) {
	dir := fixtureRepo(t)
	escape := `[{"path":"go.mod","content":"module evil\n"}]`
	client := fakeClient(t, []string{escape, escape, escape})

	_, err := PatchCore(context.Background(), client, dir, PatchCoreRequest{
		Request: "x", Reason: "y", AllowedPaths: []string{"internal/example/x.go"},
	})
	if err == nil {
		t.Fatal("expected an error - every attempt edits an out-of-scope path")
	}
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "evil") {
		t.Fatal("go.mod must not have been overwritten by an out-of-scope edit")
	}
}

// TestPatchCorePropagatesAClientCompleteFailure is PatchCore's
// counterpart to TestModifyPluginPropagatesAClientCompleteFailure.
func TestPatchCorePropagatesAClientCompleteFailure(t *testing.T) {
	dir := fixtureRepo(t)
	client := &Client{
		APIKey: "test", Model: "test", BaseURL: apiURL,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) *http.Response {
			payload, _ := json.Marshal(messagesResponse{
				Error: &struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				}{Type: "overloaded_error", Message: "the API is temporarily overloaded"},
			})
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(bytes.NewReader(payload))}
		})},
	}
	_, err := PatchCore(context.Background(), client, dir, PatchCoreRequest{
		Request: "x", Reason: "y", AllowedPaths: []string{"internal/example/x.go"},
	})
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err=%v", err)
	}
}

func TestModifyPluginToolHandlerReturnsUnavailableErrorWithoutAnAPIKey(t *testing.T) {
	t.Setenv(apiKeyEnv, "")
	handler := ModifyPluginToolHandler(t.TempDir())
	_, err := handler(context.Background(), json.RawMessage(`{"plugin":"x","request":"y"}`))
	if err == nil || !strings.Contains(err.Error(), "agent_unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestPatchCoreToolHandlerReturnsUnavailableErrorWithoutAnAPIKey(t *testing.T) {
	t.Setenv(apiKeyEnv, "")
	handler := PatchCoreToolHandler(t.TempDir())
	_, err := handler(context.Background(), json.RawMessage(`{"request":"y","allowed_paths":["a.go"]}`))
	if err == nil || !strings.Contains(err.Error(), "agent_unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestImplementToolHandlerReturnsUnavailableErrorWithoutAnAPIKey(t *testing.T) {
	t.Setenv(apiKeyEnv, "")
	handler := ImplementToolHandler(t.TempDir())
	_, err := handler(context.Background(), json.RawMessage(`{"request":"add bun support"}`))
	if err == nil || !strings.Contains(err.Error(), "agent_unavailable") {
		t.Fatalf("err=%v", err)
	}
}

// TestToolHandlersRejectInvalidJSONOnceAnAPIKeyIsConfigured covers each
// *ToolHandler's own json.Unmarshal(arguments, &args) branch, which
// TestModifyPluginToolHandlerReturnsUnavailableErrorWithoutAnAPIKey/
// TestPatchCoreToolHandlerReturnsUnavailableErrorWithoutAnAPIKey/
// TestImplementToolHandlerReturnsUnavailableErrorWithoutAnAPIKey never
// reach - it is checked only after FromEnv() succeeds. A configured (but
// fake, never-dialed) API key is enough: malformed JSON is rejected
// before Client.Complete would ever be called, so no real network access
// happens here.
func TestToolHandlersRejectInvalidJSONOnceAnAPIKeyIsConfigured(t *testing.T) {
	t.Setenv(apiKeyEnv, "test-key")
	cases := []struct {
		name    string
		handler func(context.Context, json.RawMessage) (string, error)
	}{
		{"pf_plugin_modify", ModifyPluginToolHandler(t.TempDir())},
		{"pf_core_patch", PatchCoreToolHandler(t.TempDir())},
		{"pf_implement", ImplementToolHandler(t.TempDir())},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.handler(context.Background(), json.RawMessage(`{not json`))
			if err == nil || !strings.Contains(err.Error(), "invalid_argument") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

package plugins

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateWithLanguageFamilyScaffoldsTheLangpluginShapeNotRPC covers
// F10: pf_plugin_create's "language" family used to produce the same
// sdk/plugin RPC-over-stdio scaffold as every other family, which
// `platform-factory plugin load`'s probe (langplugin.RunInspection - a
// plain "inspect --root DIR" subprocess call expecting JSON on stdout)
// can never speak to, so a freshly created language plugin always
// failed to load with "plugin failed the inspect contract". This
// confirms the family="language" branch instead produces the real
// sdk/langplugin.Dispatch shape plugins/lang-node/main.go uses.
func TestCreateWithLanguageFamilyScaffoldsTheLangpluginShapeNotRPC(t *testing.T) {
	dir := fixtureRepo(t)
	result, err := Create(context.Background(), dir, CreateRequest{
		Name:         "lisp",
		Description:  "Support Lisp applications.",
		Capabilities: []string{"detect", "build"},
		Family:       "language",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Plugin != "lisp" || result.Path != "plugins/lisp" {
		t.Fatalf("result=%+v", result)
	}

	pluginDir := filepath.Join(dir, "plugins", "lisp")

	// No plugin.json: language plugins have no manifest, no digest, no
	// signature - `platform-factory plugin load` installs a bare binary.
	if _, statErr := os.Stat(filepath.Join(pluginDir, "plugin.json")); !os.IsNotExist(statErr) {
		t.Fatalf("a language-family scaffold must not write plugin.json, stat err=%v", statErr)
	}
	// No cmd/<name>/ subdirectory: language plugins are flat, like
	// plugins/lang-node/main.go.
	if _, statErr := os.Stat(filepath.Join(pluginDir, "cmd")); !os.IsNotExist(statErr) {
		t.Fatalf("a language-family scaffold must not write a cmd/ subdirectory, stat err=%v", statErr)
	}

	mainSource, err := os.ReadFile(filepath.Join(pluginDir, "main.go"))
	if err != nil {
		t.Fatalf("main.go was not written: %v", err)
	}
	main := string(mainSource)

	// Syntactically valid Go - catches any format-string/argument
	// mismatch in renderLangMain that a passing `go vet` on this
	// package's own source wouldn't, since the mismatch would only
	// manifest in the *generated* text, not in create.go itself.
	if _, err := parser.ParseFile(token.NewFileSet(), "main.go", main, parser.AllErrors); err != nil {
		t.Fatalf("generated plugins/lisp/main.go is not valid Go: %v\n---\n%s", err, main)
	}

	// The real langplugin.Dispatch shape, not sdk/plugin's RPC server.
	for _, want := range []string{
		`"github.com/CYPT71/platform-factory/sdk/langplugin"`,
		"langplugin.Dispatch(os.Args[1:]",
		`"inspect": runInspect`,
		`"freeze": runFreeze`,
		`"build-layer": runBuildLayer`,
		`"scaffold": runScaffold`,
		"func runInspect(",
		"langplugin.ParseRootFlag(",
		"langplugin.Inspect(",
		"langplugin.WriteInspection(",
		`Language: "lisp"`,
	} {
		if !strings.Contains(main, want) {
			t.Fatalf("generated main.go missing %q:\n%s", want, main)
		}
	}
	for _, mustNotContain := range []string{
		`"github.com/CYPT71/platform-factory/sdk/plugin"`,
		"plugin.NewServer(",
		"server.Handle(",
	} {
		if strings.Contains(main, mustNotContain) {
			t.Fatalf("generated main.go for a language plugin must not reference the RPC sdk/plugin shape (%q):\n%s", mustNotContain, main)
		}
	}

	goMod, err := os.ReadFile(filepath.Join(pluginDir, "go.mod"))
	if err != nil {
		t.Fatalf("go.mod was not written: %v", err)
	}
	if !strings.Contains(string(goMod), "module github.com/CYPT71/platform-factory/plugins/lisp") {
		t.Fatalf("go.mod has the wrong module path:\n%s", goMod)
	}

	readme, err := os.ReadFile(filepath.Join(pluginDir, "README.md"))
	if err != nil {
		t.Fatalf("README.md was not written: %v", err)
	}
	if !strings.Contains(string(readme), "plugin load --from") || strings.Contains(string(readme), "plugin install") {
		t.Fatalf("README must document `plugin load`, not `plugin install` (that only ever installs the RPC family):\n%s", readme)
	}

	// F10's smaller companion bug: a fresh scaffold (of EITHER family)
	// used to be invisible to `go build`/`go test` until someone edited
	// go.work by hand - confirm Create registered it automatically.
	workContent, err := os.ReadFile(filepath.Join(dir, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workContent), "./plugins/lisp") {
		t.Fatalf("go.work was not updated with the new plugin module:\n%s", workContent)
	}
}

// TestCreateRegistersEveryFamilyInGoWork confirms the go.work fix isn't
// language-family-specific: the original, unmodified RPC scaffold path
// gets it too.
func TestCreateRegistersEveryFamilyInGoWork(t *testing.T) {
	dir := fixtureRepo(t)
	if _, err := Create(context.Background(), dir, CreateRequest{
		Name:         "widget-rpc",
		Description:  "x",
		Capabilities: []string{"detect"},
	}); err != nil {
		t.Fatal(err)
	}
	workContent, err := os.ReadFile(filepath.Join(dir, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workContent), "./plugins/widget-rpc") {
		t.Fatalf("go.work was not updated for the RPC-family scaffold:\n%s", workContent)
	}
}

// TestCreateGoWorkRegistrationIsIdempotent confirms a second Create call
// against an already-registered path (impossible via the public API
// today, since Create refuses a pre-existing plugins/<name> directory -
// but addPluginToGoWork is exercised directly here to document and lock
// in its own idempotency contract, independent of that caller-side
// guard).
func TestCreateGoWorkRegistrationIsIdempotent(t *testing.T) {
	dir := fixtureRepo(t)
	if err := addPluginToGoWork(dir, "widget"); err != nil {
		t.Fatal(err)
	}
	if err := addPluginToGoWork(dir, "widget"); err != nil {
		t.Fatalf("second call must not error: %v", err)
	}
	workContent, err := os.ReadFile(filepath.Join(dir, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(workContent), "./plugins/widget") != 1 {
		t.Fatalf("expected exactly one entry, got:\n%s", workContent)
	}
}

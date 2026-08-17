package main

import (
	"bytes"
	"context"
	"debug/elf"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/project"
)

func TestAppendRuntimeToConfigProducesAValidLoadableConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "pf.yaml")
	original := "version: 1\nlanguage: python\nprofile: \"python\"\nartifact: \"main.py\"\nisolation: container\nruntime_engine: docker\ndependency_management:\n  mode: none\n"
	if err := os.WriteFile(configPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	manifest := provisionedRuntime{
		Runtime: ".platform-factory/deps/python/runtime/python3",
		Include: []provisionedRuntimeInclude{
			{Source: ".platform-factory/deps/python/runtime/lib-0-libc.so.6", Destination: "/lib/x86_64-linux-gnu/libc.so.6", Category: "toolchain"},
			{Source: ".platform-factory/deps/python/runtime/stdlib", Destination: "/usr/local/lib/python3.12", Category: "toolchain"},
		},
	}
	if err := appendRuntimeToConfig(configPath, manifest, "main.py"); err != nil {
		t.Fatal(err)
	}

	loaded, err := project.Load(configPath)
	if err != nil {
		t.Fatalf("resulting pf.yaml does not parse/validate: %v", err)
	}
	if loaded.Config.Runtime != manifest.Runtime {
		t.Fatalf("runtime=%q", loaded.Config.Runtime)
	}
	// The interpreter starts with no guaranteed working directory, so
	// the artifact argument must be the absolute /app/... path
	// includeProject actually places it at, not the bare relative name
	// - verified by hand: a bare "main.py" argument failed with
	// "can't open file '//main.py'" when actually run.
	if len(loaded.Config.Args) != 1 || loaded.Config.Args[0] != "/app/main.py" {
		t.Fatalf("args=%v", loaded.Config.Args)
	}
	if len(loaded.Config.Include) != 2 {
		t.Fatalf("include=%v", loaded.Config.Include)
	}
	if loaded.Config.Include[0].Destination != "/lib/x86_64-linux-gnu/libc.so.6" {
		t.Fatalf("include[0]=%+v", loaded.Config.Include[0])
	}
	// The original, hand-authored content (including its language/
	// artifact fields from `pf init`) must survive untouched - this is
	// an append, not a rewrite.
	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(updated), original) {
		t.Fatalf("original content was not preserved as a prefix:\n%s", updated)
	}
}

func TestAppendRuntimeToConfigLeavesFileUntouchedOnValidationFailure(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "pf.yaml")
	original := "version: 1\nlanguage: python\nartifact: \"main.py\"\n"
	if err := os.WriteFile(configPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	// A destination containing a YAML-breaking character the quoting
	// helper doesn't neutralize would be a real bug; construct one
	// deliberately unparseable case instead - two runtime keys, which
	// project.Load's KnownFields/duplicate-document checks must reject -
	// by writing a second "runtime:" line directly into the file first,
	// simulating whatever else might make the appended result invalid.
	if err := os.WriteFile(configPath, []byte(original+"runtime: \"already-set-by-something-else\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	manifest := provisionedRuntime{Runtime: ".platform-factory/deps/python/runtime/python3"}
	err = appendRuntimeToConfig(configPath, manifest, "main.py")
	if err == nil {
		t.Fatal("expected an error for a config that would end up with a duplicate runtime key")
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("the real pf.yaml must be left untouched when the validated candidate fails to load")
	}
}

func TestHostRuntimeCandidateReportsNoneForAnUnsupportedLanguage(t *testing.T) {
	if got := hostRuntimeCandidate("node", "amd64"); got != "" {
		t.Fatalf("got=%q, want empty - no language plugin implements a runtime subcommand for node yet", got)
	}
}

func TestHostRuntimeCandidateReportsNoneWhenNothingIsOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if got := hostRuntimeCandidate("python", "amd64"); got != "" {
		t.Fatalf("got=%q, want empty - PATH was overridden to contain nothing", got)
	}
}

func TestElfMachineForArch(t *testing.T) {
	if elfMachineForArch("arm64") != elf.EM_AARCH64 {
		t.Fatal("expected arm64 to map to EM_AARCH64")
	}
	for _, arch := range []string{"amd64", "", "riscv64"} {
		if elfMachineForArch(arch) != elf.EM_X86_64 {
			t.Fatalf("expected %q to default to EM_X86_64", arch)
		}
	}
}

func TestAutoProvisionRuntimeIsANoOpWithoutARealTerminal(t *testing.T) {
	// go test's own stdin/stdout are never a real terminal, so this
	// must return immediately without blocking on a TUI, touching the
	// network, or writing anything - the same safety net that keeps
	// every other init/build TUI prompt out of CI. profile is set
	// explicitly (real pf init output always records it) so this
	// actually reaches the isatty gate instead of returning early via
	// validateBuildCapability's own "no profile recorded" compatibility
	// path - see TestAutoProvisionRuntimeIsANoOpForCompiledLanguages for
	// that other early-return path, tested on its own.
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "pf.yaml"), "version: 1\nlanguage: python\nprofile: \"python\"\nartifact: main.py\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "main.py"), "print('hi')\n", 0o644)
	var stdout, stderr bytes.Buffer
	autoProvisionRuntime(context.Background(), dir, "python", &stdout, &stderr)
	loaded, err := project.Discover(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Runtime != "" {
		t.Fatalf("expected no runtime field to be written, got %q", loaded.Config.Runtime)
	}
}

func TestAutoProvisionRuntimeIsANoOpForCompiledLanguages(t *testing.T) {
	// A compiled-language project (or any profile validateBuildCapability
	// doesn't recognize) never needs a runtime field at all - this must
	// return silently with no warning, even without a real terminal and
	// even if no plugin for "compiled" could ever be resolved.
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "pf.yaml"), "version: 1\nlanguage: compiled\nprofile: \"static\"\nartifact: app\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "app"), "binary", 0o755)
	var stdout, stderr bytes.Buffer
	autoProvisionRuntime(context.Background(), dir, "compiled", &stdout, &stderr)
	if stdout.String() != "" || stderr.String() != "" {
		t.Fatalf("expected no output for a compiled-language project, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestAutoProvisionRuntimeWarnsWhenTheLanguagePluginIsNotInstalled(t *testing.T) {
	// Overriding the plugin directory to an empty one guarantees
	// langplugin.Resolve fails deterministically here, regardless of
	// what plugins happen to be installed on the machine actually
	// running this test.
	t.Setenv("PLATFORM_FACTORY_LANG_PLUGIN_DIR", t.TempDir())
	dir := t.TempDir()
	writeProjectTestFile(t, filepath.Join(dir, "pf.yaml"), "version: 1\nlanguage: node\nprofile: \"node\"\nartifact: app.js\n", 0o644)
	writeProjectTestFile(t, filepath.Join(dir, "app.js"), "console.log('hi')\n", 0o644)
	var stdout, stderr bytes.Buffer
	autoProvisionRuntime(context.Background(), dir, "node", &stdout, &stderr)
	if !strings.Contains(stderr.String(), "isn't installed") {
		t.Fatalf("expected a warning about the missing plugin, stderr=%q", stderr.String())
	}
	loaded, err := project.Discover(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Runtime != "" {
		t.Fatalf("expected no runtime field to be written, got %q", loaded.Config.Runtime)
	}
}

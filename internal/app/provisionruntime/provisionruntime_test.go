package provisionruntime

import (
	"bytes"
	"debug/elf"
	"errors"
	"io"
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

	manifest := Manifest{
		Runtime: ".platform-factory/deps/python/runtime/python3",
		Include: []ManifestInclude{
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

	manifest := Manifest{Runtime: ".platform-factory/deps/python/runtime/python3"}
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

func noLangpluginResolve(string) (string, error) {
	return "", errors.New("resolveLangplugin should not be called by this test")
}

func TestResolveHostCandidateReportsNoneForAnUnsupportedLanguage(t *testing.T) {
	svc := New(noLangpluginResolve)
	if got := svc.ResolveHostCandidate("node", "amd64"); got != "" {
		t.Fatalf("got=%q, want empty - no language plugin implements a runtime subcommand for node yet", got)
	}
}

func TestResolveHostCandidateReportsNoneWhenNothingIsOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	svc := New(noLangpluginResolve)
	if got := svc.ResolveHostCandidate("python", "amd64"); got != "" {
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

func loadRuntimeTestProject(t *testing.T) project.Loaded {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "pf.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\nlanguage: python\nprofile: \"python\"\nartifact: \"main.py\"\nisolation: container\nruntime_engine: docker\ndependency_management:\n  mode: none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := project.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func fakeLangpluginResolver(binary string, err error) func(string) (string, error) {
	return func(string) (string, error) { return binary, err }
}

func TestProvisionRuntimeFromRootRequiresALoadedPlugin(t *testing.T) {
	loaded := loadRuntimeTestProject(t)
	svc := &service{
		execute: func(string, []string, string, io.Writer, io.Writer) error {
			t.Fatal("execute should not be called when the plugin cannot be resolved")
			return nil
		},
		resolveLangplugin: fakeLangpluginResolver("", errors.New("plugin not loaded")),
	}
	var stderr bytes.Buffer
	if _, err := svc.ProvisionFromRoot(loaded, "python", t.TempDir(), &stderr); err == nil {
		t.Fatal("expected an error for an unloaded language plugin")
	}
}

func TestProvisionRuntimeFromRootSurfacesExecuteError(t *testing.T) {
	loaded := loadRuntimeTestProject(t)
	svc := &service{
		execute: func(string, []string, string, io.Writer, io.Writer) error {
			return errors.New("boom")
		},
		resolveLangplugin: fakeLangpluginResolver("/plugin-binary", nil),
	}
	var stderr bytes.Buffer
	_, err := svc.ProvisionFromRoot(loaded, "python", t.TempDir(), &stderr)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err=%v", err)
	}
}

func TestProvisionRuntimeFromRootRejectsInvalidManifestJSON(t *testing.T) {
	loaded := loadRuntimeTestProject(t)
	svc := &service{
		execute: func(_ string, _ []string, _ string, stdout, _ io.Writer) error {
			_, _ = stdout.Write([]byte("not json"))
			return nil
		},
		resolveLangplugin: fakeLangpluginResolver("/plugin-binary", nil),
	}
	var stderr bytes.Buffer
	if _, err := svc.ProvisionFromRoot(loaded, "python", t.TempDir(), &stderr); err == nil {
		t.Fatal("expected an error for invalid plugin manifest JSON")
	}
}

func TestProvisionRuntimeFromRootRejectsEmptyRuntime(t *testing.T) {
	loaded := loadRuntimeTestProject(t)
	svc := &service{
		execute: func(_ string, _ []string, _ string, stdout, _ io.Writer) error {
			_, _ = stdout.Write([]byte(`{"runtime":""}`))
			return nil
		},
		resolveLangplugin: fakeLangpluginResolver("/plugin-binary", nil),
	}
	var stderr bytes.Buffer
	if _, err := svc.ProvisionFromRoot(loaded, "python", t.TempDir(), &stderr); err == nil {
		t.Fatal("expected an error when the plugin reports no runtime path")
	}
}

func TestProvisionRuntimeFromRootSurfacesConfigUpdateError(t *testing.T) {
	loaded := loadRuntimeTestProject(t)
	if err := os.Remove(loaded.File); err != nil {
		t.Fatal(err)
	}
	svc := &service{
		execute: func(_ string, _ []string, _ string, stdout, _ io.Writer) error {
			_, _ = stdout.Write([]byte(`{"runtime":".platform-factory/deps/python/runtime/python3"}`))
			return nil
		},
		resolveLangplugin: fakeLangpluginResolver("/plugin-binary", nil),
	}
	var stderr bytes.Buffer
	_, err := svc.ProvisionFromRoot(loaded, "python", t.TempDir(), &stderr)
	if err == nil || !strings.Contains(err.Error(), "update") {
		t.Fatalf("expected an 'update pf.yaml' error, got %v", err)
	}
}

func TestProvisionRuntimeFromRootSucceeds(t *testing.T) {
	loaded := loadRuntimeTestProject(t)
	var executedName string
	var executedArgs []string
	svc := &service{
		execute: func(name string, args []string, _ string, stdout, _ io.Writer) error {
			executedName, executedArgs = name, args
			_, _ = stdout.Write([]byte(`{"runtime":".platform-factory/deps/python/runtime/python3","include":[{"source":"x","destination":"/x","category":"toolchain"}]}`))
			return nil
		},
		resolveLangplugin: fakeLangpluginResolver("/plugin-binary", nil),
	}
	var stderr bytes.Buffer
	manifest, err := svc.ProvisionFromRoot(loaded, "python", "/image-root", &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if manifest.Runtime != ".platform-factory/deps/python/runtime/python3" || len(manifest.Include) != 1 {
		t.Fatalf("manifest=%+v", manifest)
	}
	if executedName == "" {
		t.Fatal("expected execute to be called with the resolved plugin binary")
	}
	found := false
	for i, arg := range executedArgs {
		if arg == "--image-root" && i+1 < len(executedArgs) && executedArgs[i+1] == "/image-root" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected --image-root /image-root in args, got %v", executedArgs)
	}
	reloaded, err := project.Load(loaded.File)
	if err != nil {
		t.Fatalf("resulting pf.yaml does not reload: %v", err)
	}
	if reloaded.Config.Runtime != manifest.Runtime {
		t.Fatalf("runtime not persisted: %q", reloaded.Config.Runtime)
	}
}

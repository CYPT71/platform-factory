package archtest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicAPIDomainsContainOnlyVersionDirectories(t *testing.T) {
	root, err := findWorkspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	domains, err := os.ReadDir(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range domains {
		if !domain.IsDir() {
			t.Errorf("api/%s: public API files must live in api/<domain>/<version>", domain.Name())
			continue
		}
		versions, err := os.ReadDir(filepath.Join(root, "api", domain.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, version := range versions {
			name := version.Name()
			if !version.IsDir() || len(name) < 2 || name[0] != 'v' || name[1] < '0' || name[1] > '9' {
				t.Errorf("api/%s/%s: expected a version directory", domain.Name(), version.Name())
			}
		}
	}
}

func TestForbiddenImports(t *testing.T) {
	CheckForbiddenImports(t)
}

func TestForbiddenReason(t *testing.T) {
	tests := []struct {
		name   string
		source sourceImport
		want   string
	}{
		{name: "sdk internal", source: sourceImport{PackageRel: "sdk/client", Path: repositoryModule + "/internal/new-detail"}, want: "sdk packages"},
		{name: "migration api internal", source: sourceImport{PackageRel: "api/migration/v2", Path: repositoryModule + "/internal/plugin"}, want: "public migration"},
		{name: "internal cmd", source: sourceImport{PackageRel: "internal/app/run", Path: repositoryModule + "/cmd/platform-factory"}, want: "internal packages"},
		{name: "internal api", source: sourceImport{PackageRel: "internal/plugin", Path: repositoryModule + "/api/plugin"}, want: "public api or sdk"},
		{name: "internal sdk test", source: sourceImport{PackageRel: "internal/plugin", Path: repositoryModule + "/sdk/plugin"}, want: "public api or sdk"},
		{name: "scheduler infrastructure", source: sourceImport{PackageRel: "internal/scheduler/queue", Path: repositoryModule + "/internal/oci/layout"}, want: "domain packages"},
		{name: "policy infrastructure", source: sourceImport{PackageRel: "internal/policy", Path: repositoryModule + "/internal/plugin"}, want: "domain packages"},
		{name: "executor infrastructure", source: sourceImport{PackageRel: "internal/executor/worker", Path: repositoryModule + "/internal/networking"}, want: "domain packages"},
		{name: "migration domain api", source: sourceImport{PackageRel: "internal/migration", Path: repositoryModule + "/api/migration"}, want: "public api or sdk"},
		{name: "migration domain sdk", source: sourceImport{PackageRel: "internal/migration/planner", Path: repositoryModule + "/sdk/migration"}, want: "public api or sdk"},
		{name: "migration domain plugin", source: sourceImport{PackageRel: "internal/migration", Path: repositoryModule + "/internal/plugin"}, want: "standard library"},
		{name: "migration domain cmd", source: sourceImport{PackageRel: "internal/migration", Path: repositoryModule + "/cmd/platform-factory"}, want: "standard library"},
		{name: "migration domain infrastructure", source: sourceImport{PackageRel: "internal/migration", Path: repositoryModule + "/internal/oci"}, want: "standard library"},
		{name: "migration domain third party", source: sourceImport{PackageRel: "internal/migration", Path: "example.com/dependency"}, want: "third-party"},
		{name: "migration domain core allowed", source: sourceImport{PackageRel: "internal/migration", Path: repositoryModule + "/internal/core"}},
		{name: "migration domain standard library allowed", source: sourceImport{PackageRel: "internal/migration", Path: "crypto/sha256"}},
		{name: "similar domain package allowed", source: sourceImport{PackageRel: "internal/executorish", Path: repositoryModule + "/internal/networking"}},
		{name: "migration app plugin implementation", source: sourceImport{PackageRel: "internal/app/migration/resolver", Path: repositoryModule + "/internal/plugin"}, want: "inward-facing port"},
		{name: "other app plugin implementation allowed", source: sourceImport{PackageRel: "internal/app/build", Path: repositoryModule + "/internal/plugin"}},
		{name: "platform factory concrete plugin", source: sourceImport{PackageRel: "cmd/platform-factory", Path: repositoryModule + "/plugins/kubevirt"}, want: "concrete plugin"},
		{name: "other command concrete plugin allowed", source: sourceImport{PackageRel: "cmd/plugin-debug", Path: repositoryModule + "/plugins/kubevirt"}},
		{name: "external plugin internal", source: sourceImport{Module: repositoryModule + "/plugins/a", Path: repositoryModule + "/internal/plugin"}, want: "external plugin"},
		{name: "plugin to plugin", source: sourceImport{Module: repositoryModule + "/plugins/a", Path: repositoryModule + "/plugins/b/sdk"}, want: "other plugin"},
		{name: "plugin self import allowed", source: sourceImport{Module: repositoryModule + "/plugins/a", Path: repositoryModule + "/plugins/a/internal/adapter"}},
		{name: "host plugin package self import allowed", source: sourceImport{Module: repositoryModule, PackageRel: "internal/plugin", Path: repositoryModule + "/internal/plugin/protocol"}},
		{name: "similar external path allowed", source: sourceImport{PackageRel: "sdk/client", Path: "example.com/internal/cache"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := forbiddenReason(test.source)
			if test.want == "" && got != "" {
				t.Fatalf("unexpected violation: %s", got)
			}
			if test.want != "" && !strings.Contains(got, test.want) {
				t.Fatalf("violation %q does not contain %q", got, test.want)
			}
		})
	}
}

func TestInspectWorkspaceFindsViolationsAcrossModules(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.work", "go 1.25.12\n\nuse (\n .\n ./plugins/a\n ./plugins/b\n)\n")
	writeFixture(t, root, "go.mod", "module "+repositoryModule+"\n\ngo 1.25.12\n")
	writeFixture(t, root, "sdk/bad/bad.go", "package bad\nimport _ \""+repositoryModule+"/internal/brandnew\"\n")
	writeFixture(t, root, "internal/scheduler/bad.go", "package scheduler\nimport _ \""+repositoryModule+"/internal/cache\"\n")
	writeFixture(t, root, "internal/app/migration/bad.go", "package migration\nimport _ \""+repositoryModule+"/internal/plugin\"\n")
	writeFixture(t, root, "internal/plugin/boundary_test.go", "package plugin\nimport _ \""+repositoryModule+"/sdk/plugin\"\n")
	writeFixture(t, root, "cmd/platform-factory/bad.go", "package main\nimport _ \""+repositoryModule+"/plugins/a\"\n")
	writeFixture(t, root, "plugins/a/go.mod", "module "+repositoryModule+"/plugins/a\n\ngo 1.25.12\n")
	writeFixture(t, root, "plugins/a/a.go", "package a\nimport _ \""+repositoryModule+"/plugins/b\"\n")
	writeFixture(t, root, "plugins/b/go.mod", "module "+repositoryModule+"/plugins/b\n\ngo 1.25.12\n")
	writeFixture(t, root, "plugins/b/b_test.go", "package b\nimport _ \""+repositoryModule+"/internal/plugin\"\n")

	violations, err := inspectWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(violations, "\n")
	for _, want := range []string{"sdk/bad/bad.go", "internal/scheduler/bad.go", "internal/app/migration/bad.go", "internal/plugin/boundary_test.go", "cmd/platform-factory/bad.go", "plugins/a/a.go", "plugins/b/b_test.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("violations do not mention %s:\n%s", want, got)
		}
	}
}

func TestInspectWorkspaceFailsClosedOnMalformedSource(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.work", "go 1.25.12\nuse .\n")
	writeFixture(t, root, "go.mod", "module "+repositoryModule+"\n\ngo 1.25.12\n")
	writeFixture(t, root, "sdk/broken.go", "package sdk\nimport (\n")

	_, err := inspectWorkspace(root)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("expected fail-closed parse error, got %v", err)
	}
}

func TestInspectWorkspaceFailsClosedOnModuleMetadata(t *testing.T) {
	tests := []struct {
		name    string
		goWork  string
		goMod   *string
		wantErr string
	}{
		{name: "malformed workspace", goWork: "not a go workspace\n", wantErr: "read go.work"},
		{name: "empty workspace", goWork: "go 1.25.12\n", wantErr: "declares no modules"},
		{name: "missing go.mod", goWork: "go 1.25.12\nuse .\n", wantErr: "go.mod"},
		{name: "missing module directive", goWork: "go 1.25.12\nuse .\n", goMod: stringPointer("go 1.25.12\n"), wantErr: "has no module directive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "go.work", test.goWork)
			if test.goMod != nil {
				writeFixture(t, root, "go.mod", *test.goMod)
			}
			_, err := inspectWorkspace(root)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("expected error containing %q, got %v", test.wantErr, err)
			}
		})
	}
}

func TestWorkspaceModulesResolvesAndSortsPaths(t *testing.T) {
	root := t.TempDir()
	abs := t.TempDir()
	writeFixture(t, root, "go.work", "go 1.25.12\nuse (\n ./z\n ./a\n "+filepath.ToSlash(abs)+"\n)\n")

	modules, err := workspaceModules(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 3 {
		t.Fatalf("got %d modules, want 3", len(modules))
	}
	for i := 1; i < len(modules); i++ {
		if modules[i-1] > modules[i] {
			t.Fatalf("modules are not sorted: %v", modules)
		}
	}
	for _, module := range modules {
		if !filepath.IsAbs(module) {
			t.Fatalf("module path is not absolute: %q", module)
		}
	}
}

func TestReadModulePathSupportsQuotedDirective(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module \"example.com/quoted\"\n\ngo 1.25.12\n")
	got, err := readModulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "example.com/quoted" {
		t.Fatalf("module = %q, want example.com/quoted", got)
	}
}

func TestReadModuleImportsSkipsNestedModulesAndIgnoredTrees(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "root.go", "package root\nimport _ \"example.com/root\"\n")
	writeFixture(t, root, "nested/go.mod", "module example.com/nested\n")
	writeFixture(t, root, "nested/broken.go", "package nested\nimport (\n")
	writeFixture(t, root, "vendor/broken.go", "package vendor\nimport (\n")
	writeFixture(t, root, ".hidden/broken.go", "package hidden\nimport (\n")
	writeFixture(t, root, "notes.txt", "not Go source")

	imports, err := readModuleImports(root, "example.com/root")
	if err != nil {
		t.Fatal(err)
	}
	if len(imports) != 1 || imports[0].Path != "example.com/root" || imports[0].PackageRel != "" {
		t.Fatalf("unexpected imports: %#v", imports)
	}
}

func TestReadModuleImportsFailsClosedOnBrokenNestedModuleMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires Unix-like symlink semantics")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("go.mod", filepath.Join(root, "nested", "go.mod")); err != nil {
		t.Fatal(err)
	}
	_, err := readModuleImports(root, "example.com/root")
	if err == nil || !strings.Contains(err.Error(), "nested module") {
		t.Fatalf("expected nested module inspection error, got %v", err)
	}
}

func TestFindWorkspaceRootWalksParentsAndFailsAtFilesystemRoot(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.work", "go 1.25.12\n")
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	got, err := findWorkspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantRoot {
		t.Fatalf("workspace root = %q, want %q", got, wantRoot)
	}
	if err := os.Chdir(filepath.VolumeName(root) + string(filepath.Separator)); err != nil {
		t.Fatal(err)
	}
	_, err = findWorkspaceRoot()
	if err == nil || !strings.Contains(err.Error(), "go.work not found") {
		t.Fatalf("expected missing workspace error, got %v", err)
	}
}

func stringPointer(value string) *string { return &value }

func writeFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

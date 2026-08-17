// Package archtest provides executable checks for repository dependency boundaries.
package archtest

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const repositoryModule = "github.com/CYPT71/platform-factory"

// domainInfrastructureBoundaries records the repository's established domain
// ownership rules. Go package paths carry no layer metadata, so these concrete
// package roots are the smallest enforceable representation of the boundary.
// Prefix matching keeps the rule effective for future subpackages.
var domainInfrastructureBoundaries = map[string][]string{
	"internal/scheduler": {"internal/oci", "internal/pipeline", "internal/plugin", "internal/cache", "internal/networking", "internal/hypervisor"},
	"internal/policy":    {"internal/oci", "internal/plugin", "internal/cache", "internal/networking", "internal/hypervisor"},
	"internal/executor":  {"internal/oci", "internal/pipeline", "internal/plugin", "internal/cache", "internal/networking", "internal/hypervisor"},
	"internal/assemble":  {"internal/oci"},
	"internal/project":   {"internal/oci"},
	// internal/core is the canonical domain model: it must stay independent of
	// every concrete infrastructure/backend implementation (Kubernetes, containerd,
	// KubeVirt, plugin transport, sandboxing, the CLI itself), depending only on
	// interfaces/domain contracts it defines. Audited clean; this rule exists to
	// keep it that way.
	"internal/core": {"internal/oci", "internal/pipeline", "internal/plugin", "internal/cache", "internal/networking", "internal/hypervisor", "internal/executor", "internal/microvm", "internal/ociruntime"},
}

type workspaceJSON struct {
	Use []struct {
		DiskPath string
	}
}

type sourceImport struct {
	Module     string
	File       string
	PackageRel string
	Path       string
}

// CheckForbiddenImports verifies every module listed by go.work. Inspection
// errors are fatal: an unreadable or malformed package must never make an
// architecture check silently succeed.
func CheckForbiddenImports(t *testing.T) {
	t.Helper()
	root, err := findWorkspaceRoot()
	if err != nil {
		t.Fatalf("architecture inspection failed: %v", err)
	}

	violations, err := inspectWorkspace(root)
	if err != nil {
		t.Fatalf("architecture inspection failed: %v", err)
	}
	if len(violations) != 0 {
		t.Errorf("architecture violations found:\n%s", strings.Join(violations, "\n"))
	}
}

func inspectWorkspace(root string) ([]string, error) {
	modules, err := workspaceModules(root)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, moduleDir := range modules {
		modulePath, err := readModulePath(filepath.Join(moduleDir, "go.mod"))
		if err != nil {
			return nil, err
		}
		imports, err := readModuleImports(moduleDir, modulePath)
		if err != nil {
			return nil, err
		}
		for _, source := range imports {
			if reason := forbiddenReason(source); reason != "" {
				rel, err := filepath.Rel(root, source.File)
				if err != nil {
					return nil, fmt.Errorf("resolve source path %q: %w", source.File, err)
				}
				violations = append(violations, fmt.Sprintf("%s imports %s: %s", filepath.ToSlash(rel), source.Path, reason))
			}
		}
	}
	sort.Strings(violations)
	return violations, nil
}

func forbiddenReason(source sourceImport) string {
	internal := repositoryModule + "/internal"
	cmd := repositoryModule + "/cmd"
	api := repositoryModule + "/api"
	sdk := repositoryModule + "/sdk"
	plugins := repositoryModule + "/plugins/"
	pkg := strings.Trim(source.PackageRel, "/")
	if hasPackagePrefix(pkg, "internal") && (hasImportPrefix(source.Path, api) || hasImportPrefix(source.Path, sdk)) {
		return "internal packages and their tests must not depend on public api or sdk adapters"
	}
	if hasPackagePrefix(pkg, "internal/migration") {
		if strings.HasPrefix(source.Path, repositoryModule+"/") && !hasImportPrefix(source.Path, internal+"/core") {
			return "migration domain may depend only on the standard library and internal/core"
		}
		if first := strings.SplitN(source.Path, "/", 2)[0]; strings.Contains(first, ".") && !strings.HasPrefix(source.Path, repositoryModule+"/") {
			return "migration domain may not depend on third-party packages"
		}
	}
	for domain, infrastructure := range domainInfrastructureBoundaries {
		if !hasPackagePrefix(pkg, domain) {
			continue
		}
		for _, target := range infrastructure {
			if hasImportPrefix(source.Path, repositoryModule+"/"+target) {
				return "domain packages must not depend on concrete infrastructure packages"
			}
		}
	}

	if (pkg == "sdk" || strings.HasPrefix(pkg, "sdk/")) && hasImportPrefix(source.Path, internal) {
		return "sdk packages must not depend on internal packages"
	}
	if (pkg == "api/migration" || strings.HasPrefix(pkg, "api/migration/") ||
		pkg == "sdk/migration" || strings.HasPrefix(pkg, "sdk/migration/")) && hasImportPrefix(source.Path, internal) {
		return "public migration contracts and helpers must not depend on internal packages"
	}
	if (pkg == "internal" || strings.HasPrefix(pkg, "internal/")) && hasImportPrefix(source.Path, cmd) {
		return "internal packages must not depend on command composition roots"
	}
	if hasPackagePrefix(pkg, "internal/app/migration") && hasImportPrefix(source.Path, internal+"/plugin") {
		return "migration application orchestration must use an inward-facing port instead of the plugin implementation"
	}
	if hasPackagePrefix(pkg, "cmd/platform-factory") && strings.HasPrefix(source.Path, plugins) {
		return "the platform-factory composition root must not import concrete plugin modules"
	}

	if strings.HasPrefix(source.Module, plugins) {
		if hasImportPrefix(source.Path, internal) {
			return "external plugin modules must not depend on host internal packages"
		}
		if strings.HasPrefix(source.Path, plugins) && !hasImportPrefix(source.Path, source.Module) {
			return "plugin modules must not import other plugin modules"
		}
	}

	return ""
}

// Pair-specific plugin semantics cannot be proven from imports alone. In
// particular, scanning identifiers or string literals would be both trivially
// bypassable and prone to flag legitimate user-facing plugin references. That
// invariant belongs in resolver/conformance behavior tests; this package only
// enforces dependency boundaries that Go syntax can establish reliably.

func hasPackagePrefix(packagePath, prefix string) bool {
	return packagePath == prefix || strings.HasPrefix(packagePath, prefix+"/")
}

func hasImportPrefix(importPath, prefix string) bool {
	return importPath == prefix || strings.HasPrefix(importPath, prefix+"/")
}

func workspaceModules(root string) ([]string, error) {
	cmd := exec.Command("go", "work", "edit", "-json")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("read go.work: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var work workspaceJSON
	if err := json.Unmarshal(output, &work); err != nil {
		return nil, fmt.Errorf("decode go.work: %w", err)
	}
	if len(work.Use) == 0 {
		return nil, fmt.Errorf("go.work declares no modules")
	}
	modules := make([]string, 0, len(work.Use))
	for _, use := range work.Use {
		path := use.DiskPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace module %q: %w", use.DiskPath, err)
		}
		modules = append(modules, filepath.Clean(path))
	}
	sort.Strings(modules)
	return modules, nil
}

func readModulePath(goMod string) (string, error) {
	data, err := os.ReadFile(goMod)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", goMod, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`), nil
		}
	}
	return "", fmt.Errorf("%s has no module directive", goMod)
}

func readModuleImports(moduleDir, modulePath string) ([]sourceImport, error) {
	var imports []sourceImport
	err := filepath.WalkDir(moduleDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != moduleDir {
				if entry.Name() == "vendor" || strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				} else if !os.IsNotExist(err) {
					return fmt.Errorf("inspect nested module at %s: %w", path, err)
				}
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		rel, err := filepath.Rel(moduleDir, filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("resolve package for %s: %w", path, err)
		}
		if rel == "." {
			rel = ""
		}
		for _, spec := range file.Decls {
			decl, ok := spec.(*ast.GenDecl)
			if !ok || decl.Tok != token.IMPORT {
				continue
			}
			for _, item := range decl.Specs {
				imp := item.(*ast.ImportSpec)
				value, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return fmt.Errorf("decode import in %s: %w", path, err)
				}
				imports = append(imports, sourceImport{Module: modulePath, File: path, PackageRel: filepath.ToSlash(rel), Path: value})
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect module %s: %w", moduleDir, err)
	}
	return imports, nil
}

func findWorkspaceRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.work")); err == nil {
			return wd, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", fmt.Errorf("go.work not found")
		}
		wd = parent
	}
}

// Package core implements the pf_core_inspect/pf_core_validate/
// pf_core_patch tools and the pf://core, pf://core/packages resources:
// a read-only map of this repository's internal/ domain packages plus
// bounded, scoped write primitives for proposing a core change. Like
// its sibling packages (project, plugins, git), it has no dependency on
// internal/mcp itself - handlers are plain functions over stdlib types,
// wired into an *mcp.Server by internal/mcp's own registration code.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CYPT71/platform-factory/internal/mcp/docutil"
)

// areaPackages maps each pf_core_inspect "area" to the internal/
// packages that implement it. Grounded in this repository's real
// internal/ directory names (confirmed by directory listing, not
// invented) - see docs/mcp.md for the same mapping in prose.
var areaPackages = map[string][]string{
	"marketplace":  {"internal/marketplace", "cmd/tui/marketplacetui"},
	"runtime":      {"internal/runtime", "internal/ociruntime", "internal/executor", "internal/guest", "internal/guesttransport"},
	"builder":      {"internal/oci", "internal/layout", "internal/rootfs", "internal/assemble", "internal/pipeline", "internal/scheduler"},
	"registry":     {"internal/registry"},
	"supply-chain": {"internal/attestation", "internal/provenance", "internal/sbom", "internal/signing", "internal/policy"},
	"microvm":      {"internal/microvm", "internal/hypervisor", "internal/directboot", "internal/vmdisk"},
	"cli":          {"cmd/platform-factory"},
}

func validAreas() []string {
	areas := make([]string, 0, len(areaPackages)+1)
	for area := range areaPackages {
		areas = append(areas, area)
	}
	areas = append(areas, "all")
	sort.Strings(areas)
	return areas
}

// PackageInfo is one package's inspection detail.
type PackageInfo struct {
	Package   string   `json:"package"`
	Doc       string   `json:"doc,omitempty"`
	Files     []string `json:"files"`
	TestFiles []string `json:"test_files,omitempty"`
}

// AreaInspection is the pf_core_inspect payload for one area.
type AreaInspection struct {
	Area          string        `json:"area"`
	Packages      []PackageInfo `json:"packages"`
	Compatibility string        `json:"compatibility"`
}

// compatibilityNote is deliberately a static, hand-written summary of
// internal/archtest's real boundary rules (internal/archtest/archtest.go,
// domainInfrastructureBoundaries and forbiddenReason) rather than an
// attempt to reflect its unexported rule tables at runtime - those
// tables are not exported, and re-deriving them by parsing the source
// would drift the moment archtest.go changes without this string
// changing too. The authoritative, always-current check is the command
// named below, which pf_core_validate and pf_core_patch's self-check
// both actually run.
const compatibilityNote = "General boundaries enforced by internal/archtest (go test ./internal/archtest/...): " +
	"sdk/* and api/* must not import internal/*; plugins/* must not import internal/* or each other; " +
	"cmd/platform-factory must not import plugins/* directly; internal/migration may only import the " +
	"standard library and internal/core. Run the command above for the authoritative, current answer - " +
	"this note can drift, that command cannot."

// Inspect gathers packages, docs, and file listings for one area (or
// every internal/ package when area is "all" or empty).
func Inspect(repoRoot, area string) (AreaInspection, error) {
	if area == "" {
		area = "all"
	}
	var packageDirs []string
	if area == "all" {
		packageDirs = allInternalPackages(repoRoot)
	} else {
		dirs, ok := areaPackages[area]
		if !ok {
			return AreaInspection{}, fmt.Errorf("unknown area %q; valid areas: %s", area, strings.Join(validAreas(), ", "))
		}
		packageDirs = dirs
	}

	inspection := AreaInspection{Area: area, Compatibility: compatibilityNote}
	for _, relative := range packageDirs {
		info, err := inspectPackage(repoRoot, relative)
		if err != nil {
			continue // a package that fails to read is skipped, not fatal to the whole inspection
		}
		inspection.Packages = append(inspection.Packages, info)
	}
	sort.Slice(inspection.Packages, func(i, j int) bool { return inspection.Packages[i].Package < inspection.Packages[j].Package })
	return inspection, nil
}

func allInternalPackages(repoRoot string) []string {
	var dirs []string
	entries, err := os.ReadDir(filepath.Join(repoRoot, "internal"))
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, "internal/"+entry.Name())
		}
	}
	return dirs
}

func inspectPackage(repoRoot, relative string) (PackageInfo, error) {
	dir := filepath.Join(repoRoot, filepath.FromSlash(relative))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return PackageInfo{}, err
	}
	info := PackageInfo{Package: relative, Doc: docutil.PackageDoc(dir)}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			info.TestFiles = append(info.TestFiles, name)
		} else {
			info.Files = append(info.Files, name)
		}
	}
	sort.Strings(info.Files)
	sort.Strings(info.TestFiles)
	return info, nil
}

type inspectArguments struct {
	Area string `json:"area"`
}

// InspectToolHandler returns the pf_core_inspect handler.
func InspectToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args inspectArguments
		if len(arguments) > 0 && string(arguments) != "{}" {
			if err := json.Unmarshal(arguments, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
		}
		inspection, err := Inspect(repoRoot, args.Area)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(inspection, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

// CoreResourceHandler returns the pf://core resource handler: every
// area's package list, without per-file detail (that level of detail is
// what pf://core/packages and pf_core_inspect are for).
func CoreResourceHandler(repoRoot string) func(context.Context) (string, string, error) {
	return func(ctx context.Context) (string, string, error) {
		areas := make(map[string][]string, len(areaPackages))
		for area, dirs := range areaPackages {
			areas[area] = dirs
		}
		encoded, err := json.MarshalIndent(map[string]any{
			"areas":         areas,
			"compatibility": compatibilityNote,
		}, "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(encoded), "application/json", nil
	}
}

// PackagesResourceHandler returns the pf://core/packages resource
// handler: every internal/ package with its doc and file counts.
func PackagesResourceHandler(repoRoot string) func(context.Context) (string, string, error) {
	return func(ctx context.Context) (string, string, error) {
		inspection, err := Inspect(repoRoot, "all")
		if err != nil {
			return "", "", err
		}
		encoded, err := json.MarshalIndent(inspection.Packages, "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(encoded), "application/json", nil
	}
}

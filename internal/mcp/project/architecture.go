package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/CYPT71/platform-factory/internal/mcp/docutil"
)

// PackageSummary is one internal/<name> package's grounded, one-line
// description, taken directly from that package's own doc comment - not
// hand-curated prose that could drift from the real code.
type PackageSummary struct {
	Package string `json:"package"`
	Doc     string `json:"doc,omitempty"`
}

// Architecture is the pf_core_inspect / pf://architecture payload: every
// internal/ package's doc summary, plus the CLI's own top-level command
// names, both read from the repository rather than described from
// memory.
type Architecture struct {
	Module   string           `json:"module"`
	Commands []string         `json:"cli_commands"`
	Packages []PackageSummary `json:"internal_packages"`
}

// GatherArchitecture walks repoRoot/internal, collecting each
// package's doc comment, and repoRoot/cmd for the CLI's own command
// entry points.
func GatherArchitecture(repoRoot string) Architecture {
	arch := Architecture{Module: moduleName(repoRoot)}

	internalDir := filepath.Join(repoRoot, "internal")
	if entries, err := os.ReadDir(internalDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			arch.Packages = append(arch.Packages, PackageSummary{
				Package: "internal/" + entry.Name(),
				Doc:     docutil.PackageDoc(filepath.Join(internalDir, entry.Name())),
			})
		}
	}
	sort.Slice(arch.Packages, func(i, j int) bool { return arch.Packages[i].Package < arch.Packages[j].Package })

	cmdDir := filepath.Join(repoRoot, "cmd")
	if entries, err := os.ReadDir(cmdDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				arch.Commands = append(arch.Commands, entry.Name())
			}
		}
	}
	sort.Strings(arch.Commands)

	return arch
}

// ArchitectureResourceHandler returns the pf://architecture resource
// handler.
func ArchitectureResourceHandler(repoRoot string) func(context.Context) (string, string, error) {
	return func(ctx context.Context) (string, string, error) {
		encoded, err := json.MarshalIndent(GatherArchitecture(repoRoot), "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(encoded), "application/json", nil
	}
}

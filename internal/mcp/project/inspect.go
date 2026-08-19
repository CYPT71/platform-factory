// Package project implements the pf_project_inspect tool and the
// pf://project / pf://architecture resources: a read-only summary of
// this repository's identity (module, version, git state) and its
// major components. It has no dependency on the internal/mcp package
// itself - handlers are plain functions over stdlib types, wired into
// an *mcp.Server by internal/mcp's own registration code - so this
// package stays reusable and free of an import cycle back to its
// caller.
package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CYPT71/platform-factory/internal/mcp/git"
	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

// Info is the pf_project_inspect / pf://project payload.
type Info struct {
	Name               string   `json:"name"`
	Version            string   `json:"version"`
	RepoRoot           string   `json:"repo_root"`
	Module             string   `json:"module"`
	Branch             string   `json:"branch,omitempty"`
	Dirty              bool     `json:"dirty"`
	Components         []string `json:"components,omitempty"`
	ValidationCommands []string `json:"validation_commands"`
}

// validationCommands lists this repository's own real, current
// verification commands (matching .github/workflows/ci-quality.yml and
// ci-security.yml) - not invented ones - so a caller knows exactly what
// "passing validation" means here before proposing a change.
var validationCommands = []string{
	"gofmt -l .",
	"go vet ./...",
	"go test ./...",
	"go test -race ./...",
	"go test ./internal/archtest/...",
	"govulncheck ./...",
}

// topLevelComponents lists the directories under repoRoot whose name
// carries architectural meaning for this project (cmd entry points, the
// internal domain packages, the SDK plugins build against, and the
// out-of-process plugins themselves) - read from disk each call so it
// never drifts from the real tree.
func topLevelComponents(repoRoot string) []string {
	var components []string
	for _, group := range []string{"cmd", "internal", "sdk", "plugins"} {
		entries, err := os.ReadDir(filepath.Join(repoRoot, group))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				components = append(components, group+"/"+entry.Name())
			}
		}
	}
	sort.Strings(components)
	return components
}

func moduleName(repoRoot string) string {
	data, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// Inspect gathers the current project snapshot. depth "detailed"
// additionally populates Components; "summary" (the default for an
// empty value) omits them for a smaller response.
func Inspect(ctx context.Context, repoRoot, version, depth string) (Info, error) {
	info := Info{
		Name:               "platform-factory",
		Version:            version,
		RepoRoot:           repoRoot,
		Module:             moduleName(repoRoot),
		ValidationCommands: validationCommands,
	}
	if status, err := git.New(repoRoot).Status(ctx); err == nil {
		info.Branch = status.Branch
		info.Dirty = status.Dirty
	}
	if depth == "detailed" {
		info.Components = topLevelComponents(repoRoot)
	}
	return info, nil
}

type inspectArguments struct {
	Depth string `json:"depth"`
}

// InspectToolHandler returns the pf_project_inspect handler bound to
// repoRoot/version. The MCP wiring layer supplies the strict-decoding
// and toolError translation policy this handler's caller applies
// uniformly across every tool in the server.
func InspectToolHandler(repoRoot, version string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args inspectArguments
		if len(arguments) > 0 && string(arguments) != "{}" {
			if err := json.Unmarshal(arguments, &args); err != nil {
				return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
			}
		}
		if args.Depth == "" {
			args.Depth = "summary"
		}
		if args.Depth != "summary" && args.Depth != "detailed" {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "depth must be %q or %q", "summary", "detailed")
		}
		info, err := Inspect(ctx, repoRoot, version, args.Depth)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

// ProjectResourceHandler returns the pf://project resource handler:
// always the detailed snapshot, since a resource read (unlike a tool
// call) has no way to pass a depth argument.
func ProjectResourceHandler(repoRoot, version string) func(context.Context) (string, string, error) {
	return func(ctx context.Context) (string, string, error) {
		info, err := Inspect(ctx, repoRoot, version, "detailed")
		if err != nil {
			return "", "", err
		}
		encoded, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return "", "", err
		}
		return string(encoded), "application/json", nil
	}
}

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

// ResolveScopedPath resolves a repo-relative path and guarantees the
// result cannot escape repoRoot - the same defense-in-depth shape this
// codebase's own internal/rootfs uses for confined archive extraction,
// applied here to confine every pf_core_patch write to the repository
// it was given. It rejects: absolute paths, ".." segments (even ones
// that would net out to a valid location - the check is on the
// requested path shape, not just the final resolved location), and
// symlinks anywhere in the resolved parent chain that would otherwise
// let a write land outside repoRoot.
func ResolveScopedPath(repoRoot, relative string) (string, error) {
	if relative == "" {
		return "", toolerror.New(toolerror.ErrInvalidArgument, "path must not be empty")
	}
	cleaned := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", toolerror.New(toolerror.ErrPathOutsideRepo, "path %q must be a repository-relative path with no .. segments", relative)
	}

	absoluteRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}

	full := filepath.Join(absoluteRoot, cleaned)
	// Walk up from full to the first existing ancestor, resolving
	// symlinks along that existing prefix only - the file/dir being
	// created does not exist yet, so EvalSymlinks itself cannot be
	// called on the full path for a write that doesn't exist yet.
	existingAncestor := full
	for {
		if _, err := os.Lstat(existingAncestor); err == nil {
			break
		}
		parent := filepath.Dir(existingAncestor)
		if parent == existingAncestor {
			break
		}
		existingAncestor = parent
	}
	resolvedAncestor, err := filepath.EvalSymlinks(existingAncestor)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", relative, err)
	}
	if resolvedAncestor != resolvedRoot && !strings.HasPrefix(resolvedAncestor, resolvedRoot+string(filepath.Separator)) {
		return "", toolerror.New(toolerror.ErrPathOutsideRepo, "path %q resolves outside the repository", relative)
	}
	// Re-derive full under the resolved ancestor so a symlink earlier in
	// the chain cannot redirect the final write either.
	suffix := strings.TrimPrefix(full, existingAncestor)
	return resolvedAncestor + suffix, nil
}

// ReadFileResult is the pf_core_patch "read" operation's payload.
type ReadFileResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ReadFile reads one repo-relative file, confined to repoRoot.
func ReadFile(repoRoot, relative string) (ReadFileResult, error) {
	full, err := ResolveScopedPath(repoRoot, relative)
	if err != nil {
		return ReadFileResult{}, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return ReadFileResult{}, toolerror.New(toolerror.ErrNotFound, "file %q does not exist", relative)
		}
		return ReadFileResult{}, err
	}
	return ReadFileResult{Path: relative, Content: string(data)}, nil
}

// WriteFileResult is the pf_core_patch "write" operation's payload.
type WriteFileResult struct {
	Path    string `json:"path"`
	Written bool   `json:"written"`
}

// WriteFile writes content to one repo-relative file, confined to
// repoRoot, creating parent directories as needed. It refuses to follow
// or replace a symlink at the target path - a write always lands on a
// real regular file, never redirected by a link an attacker (or a
// confused agent) planted first.
func WriteFile(repoRoot, relative, content string) (WriteFileResult, error) {
	full, err := ResolveScopedPath(repoRoot, relative)
	if err != nil {
		return WriteFileResult{}, err
	}
	if info, err := os.Lstat(full); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return WriteFileResult{}, toolerror.New(toolerror.ErrInvalidArgument, "refusing to write through a symlink at %q", relative)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return WriteFileResult{}, err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return WriteFileResult{}, err
	}
	return WriteFileResult{Path: relative, Written: true}, nil
}

type readFileArguments struct {
	Path string `json:"path"`
}

// ReadFileToolHandler returns the pf_core_read_file handler.
func ReadFileToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args readFileArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		result, err := ReadFile(repoRoot, args.Path)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

type writeFileArguments struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteFileToolHandler returns the pf_core_write_file handler: the
// bounded, scoped write primitive pf_core_patch's own free-text mode
// (internal/mcp/agent) is built on top of, and that a client-orchestrated
// caller can use directly instead.
func WriteFileToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var args writeFileArguments
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		result, err := WriteFile(repoRoot, args.Path, args.Content)
		if err != nil {
			return "", err
		}
		encoded, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

// SelfCheck runs the same archtest boundary suite pf_core_validate's
// "full" profile runs, scoped down to just that one package - the
// pre-PR gate every core-touching workflow (pf_core_patch, pf_implement)
// runs before proposing a branch, so a boundary violation is caught
// here instead of surfacing later as a failing CI job on the PR.
func SelfCheck(ctx context.Context, repoRoot string) StepResult {
	step := runCommand(ctx, repoRoot, "go", "test", "./internal/archtest/...")
	step.Name = "pre-PR self-check: go test ./internal/archtest/..."
	return step
}

// SelfCheckToolHandler returns the pf_core_self_check handler.
func SelfCheckToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		step := SelfCheck(ctx, repoRoot)
		encoded, err := json.MarshalIndent(step, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

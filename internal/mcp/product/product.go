// Package product exposes platform-factory's own end-user product
// commands (init, build, publish, deploy, status, doctor, detect,
// verify/inspect) as MCP tools - the CLI capability an MCP client uses
// this server for beyond just inspecting/modifying platform-factory's
// own source, letting it drive the same "build and ship a project"
// workflow a human would from a terminal.
//
// Every tool here re-execs the currently-running platform-factory
// binary itself (os.Executable(), the same self-re-exec convention
// cmd/platform-factory/main.go already uses for its sandbox/rlimit
// helpers) with a FIXED subcommand name and an argument array built
// from a strict, named JSON schema - never a shell string, and never a
// caller-chosen subcommand. optionalArgs lets a caller pass additional
// flags the schema doesn't model by name; it is still one argument per
// array element handed straight to exec.Command, so it can smuggle an
// unexpected flag into the fixed subcommand but never a second command,
// a shell metacharacter, or an arbitrary executable.
package product

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/mcp/core"
	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

// commandTimeout bounds every product-command invocation - generous for
// a real build, but finite so a stuck subprocess cannot hang the whole
// MCP session (see internal/mcp/server.go's single-request-at-a-time
// dispatch loop).
const commandTimeout = 10 * time.Minute

// Result is what every tool in this package returns: the real exit
// code and both output streams, never just "it worked."
type Result struct {
	Command  string   `json:"command"`
	Args     []string `json:"args"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
}

// selfExecutable resolves the currently-running platform-factory
// binary's own path once - the same binary this MCP server is part of,
// so `pf_init`/`pf_build`/etc. always run the exact build a caller is
// already talking to, never a possibly-different `pf` that happens to
// be first on PATH.
func selfExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve the running platform-factory binary: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve the running platform-factory binary: %w", err)
	}
	return resolved, nil
}

// run re-execs the running binary with subcommand and args, confined to
// repoRoot as its working directory, and returns the real result -
// never turning a non-zero exit into a Go error, since "the command ran
// and failed" is exactly the information a caller needs back, not a
// truncated error string.
func run(ctx context.Context, repoRoot, subcommand string, args []string) (Result, error) {
	self, err := selfExecutable()
	if err != nil {
		return Result{}, err
	}
	full := append([]string{subcommand}, args...)
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, self, full...)
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	result := Result{
		Command: subcommand, Args: args,
		Stdout: stdout.String(), Stderr: stderr.String(),
	}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	if runErr != nil {
		return Result{}, fmt.Errorf("run platform-factory %s: %w", subcommand, runErr)
	}
	return result, nil
}

// scopedRelative validates a caller-supplied path argument is confined
// to repoRoot (see internal/mcp/core.ResolveScopedPath) and returns it
// unchanged for use as a CLI argument - platform-factory's own
// subprocess still receives the original relative form, since that is
// what every one of these commands' own --help text documents, but the
// confinement check runs first so a path that would resolve outside
// repoRoot is rejected before the subprocess ever starts.
func scopedRelative(repoRoot, relative string) (string, error) {
	if relative == "" {
		return "", nil
	}
	if _, err := core.ResolveScopedPath(repoRoot, relative); err != nil {
		return "", err
	}
	return relative, nil
}

// resolveProjectRoot returns the effective root a single tool call
// should operate against: repoRoot unchanged when projectRoot is empty
// (today's behavior, preserved exactly for backward compatibility - see
// this package's doc comment and cmd/platform-factory/mcp.go's go.mod
// gate, neither of which apply here), or projectRoot itself - validated
// and resolved to an absolute, cleaned path - when a caller supplies it.
//
// Unlike repoRoot, projectRoot is deliberately NOT required to be a Go
// module root and NOT confined to live inside repoRoot: the whole point
// of this argument is letting a caller target an independent end-user
// project (a plain Node/Python/whatever app with no go.mod at all,
// anywhere on disk) rather than platform-factory's own checkout. Its
// only safety check is that it resolves to an existing directory - a
// relative path is rejected outright, since "relative to what" is
// ambiguous for a stateless tool call with no notion of a client-side
// working directory.
func resolveProjectRoot(repoRoot, projectRoot string) (string, error) {
	if projectRoot == "" {
		return repoRoot, nil
	}
	if !filepath.IsAbs(projectRoot) {
		return "", toolerror.New(toolerror.ErrInvalidArgument, "project_root %q must be an absolute path", projectRoot)
	}
	abs, err := filepath.Abs(filepath.Clean(projectRoot))
	if err != nil {
		return "", toolerror.New(toolerror.ErrInvalidArgument, "resolve project_root %q: %v", projectRoot, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", toolerror.New(toolerror.ErrInvalidArgument, "project_root %q: %v", projectRoot, err)
	}
	if !info.IsDir() {
		return "", toolerror.New(toolerror.ErrInvalidArgument, "project_root %q is not a directory", projectRoot)
	}
	return abs, nil
}

func encode(result Result) (string, error) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// boolFlag appends --name only when set is true.
func boolFlag(args []string, name string, set bool) []string {
	if set {
		return append(args, name)
	}
	return args
}

// stringFlag appends --name value only when value is non-empty.
func stringFlag(args []string, name, value string) []string {
	if value == "" {
		return args
	}
	return append(args, name, value)
}

// validExtraArgs rejects an extra_args entry that looks like an attempt
// to smuggle a second command or a NUL byte - it is still just one more
// argv element either way, never shell-interpreted, but this catches
// the obviously-wrong-intent cases early with a clear error instead of
// letting the subprocess itself reject them less legibly.
func validExtraArgs(args []string) error {
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return toolerror.New(toolerror.ErrInvalidArgument, "extra_args entries must not contain NUL bytes")
		}
	}
	return nil
}

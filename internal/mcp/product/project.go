package product

import (
	"context"
	"encoding/json"
	"errors"
)

type projectArguments struct {
	Action       string   `json:"action"`
	Directory    string   `json:"directory"`
	Config       string   `json:"config"`
	DryRun       bool     `json:"dry_run"`
	Write        bool     `json:"write"`
	MaxWallClock string   `json:"max_wall_clock"`
	MaxCPU       string   `json:"max_cpu"`
	MaxMemory    string   `json:"max_memory"`
	ExtraArgs    []string `json:"extra_args"`
}

var validProjectActions = map[string]bool{
	"show": true, "plan": true, "freeze": true, "build": true,
	"run": true, "launch": true, "migrate": true,
}

var errInvalidProjectAction = errors.New("action must be one of: show, plan, freeze, build, run, launch, migrate")

// ProjectToolHandler returns the pf_project handler: `platform-factory
// project <action>`, driving a pf.yaml project's full lifecycle rather
// than the low-level single-executable/layout operations pf_build and
// pf_publish expose. "run" is the only command in this whole tool
// package that actually starts a built image - it shells to the host's
// docker/podman (or a native microVM runtime) the same way `platform-
// factory run`/`launch` do from a terminal, so it requires that runtime
// to be reachable from wherever this MCP server process itself runs.
func ProjectToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var a projectArguments
		if err := json.Unmarshal(arguments, &a); err != nil {
			return "", err
		}
		if !validProjectActions[a.Action] {
			return "", errInvalidProjectAction
		}
		if err := validExtraArgs(a.ExtraArgs); err != nil {
			return "", err
		}
		directory, err := scopedRelative(repoRoot, a.Directory)
		if err != nil {
			return "", err
		}
		config, err := scopedRelative(repoRoot, a.Config)
		if err != nil {
			return "", err
		}

		args := []string{a.Action}
		args = stringFlag(args, "--config", config)
		if a.Action == "freeze" || a.Action == "build" {
			args = boolFlag(args, "--dry-run", a.DryRun)
		}
		if a.Action == "build" {
			args = stringFlag(args, "--max-wall-clock", a.MaxWallClock)
			args = stringFlag(args, "--max-cpu", a.MaxCPU)
			args = stringFlag(args, "--max-memory", a.MaxMemory)
		}
		if a.Action == "migrate" {
			args = boolFlag(args, "--write", a.Write)
		}
		args = append(args, a.ExtraArgs...)
		if directory != "" {
			args = append(args, directory)
		}

		result, err := run(ctx, repoRoot, "project", args)
		if err != nil {
			return "", err
		}
		return encode(result)
	}
}

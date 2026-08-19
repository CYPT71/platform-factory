package product

import (
	"context"
	"encoding/json"

	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

type statusArguments struct {
	Directory   string `json:"directory"`
	Format      string `json:"format"`
	ProjectRoot string `json:"project_root"`
}

// StatusToolHandler returns the pf_status handler: `platform-factory
// status`, reporting build/publish/deploy state and the next safe
// command.
func StatusToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var a statusArguments
		if len(arguments) > 0 && string(arguments) != "{}" {
			if err := json.Unmarshal(arguments, &a); err != nil {
				return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
			}
		}
		root, err := resolveProjectRoot(repoRoot, a.ProjectRoot)
		if err != nil {
			return "", err
		}
		directory, err := scopedRelative(root, a.Directory)
		if err != nil {
			return "", err
		}
		format := a.Format
		if format == "" {
			format = "json"
		}
		args := []string{"--format", format}
		if directory != "" {
			args = append(args, directory)
		}
		result, err := run(ctx, root, "status", args)
		if err != nil {
			return "", err
		}
		return encode(result)
	}
}

type doctorArguments struct {
	Scope       string `json:"scope"`
	ProjectRoot string `json:"project_root"`
}

// DoctorToolHandler returns the pf_doctor handler: `platform-factory
// doctor --json`, checking local tools, runtimes, and hardware support.
func DoctorToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var a doctorArguments
		if len(arguments) > 0 && string(arguments) != "{}" {
			if err := json.Unmarshal(arguments, &a); err != nil {
				return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
			}
		}
		root, err := resolveProjectRoot(repoRoot, a.ProjectRoot)
		if err != nil {
			return "", err
		}
		args := []string{"--json"}
		switch a.Scope {
		case "", "all":
		case "build", "publish", "deploy":
			args = append(args, a.Scope)
		default:
			return "", errInvalidScope
		}
		result, err := run(ctx, root, "doctor", args)
		if err != nil {
			return "", err
		}
		return encode(result)
	}
}

var errInvalidScope = toolerror.New(toolerror.ErrInvalidArgument, "scope must be empty, all, build, publish, or deploy")

type detectArguments struct {
	Path            string `json:"path"`
	Format          string `json:"format"`
	AcceptAmbiguous bool   `json:"accept_ambiguous"`
	ProjectRoot     string `json:"project_root"`
}

// DetectToolHandler returns the pf_detect handler: `platform-factory
// detect`, classifying an application input without executing it.
func DetectToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var a detectArguments
		if err := json.Unmarshal(arguments, &a); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		root, err := resolveProjectRoot(repoRoot, a.ProjectRoot)
		if err != nil {
			return "", err
		}
		path, err := scopedRelative(root, a.Path)
		if err != nil {
			return "", err
		}
		if path == "" {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "path is required")
		}
		format := a.Format
		if format == "" {
			format = "json"
		}
		args := []string{"--format", format}
		args = boolFlag(args, "--accept-ambiguous", a.AcceptAmbiguous)
		args = append(args, path)
		result, err := run(ctx, root, "detect", args)
		if err != nil {
			return "", err
		}
		return encode(result)
	}
}

type layoutArguments struct {
	Layout      string `json:"layout"`
	Format      string `json:"format"`
	ProjectRoot string `json:"project_root"`
}

// VerifyToolHandler returns the pf_verify handler: `platform-factory
// verify`, strictly validating a local OCI layout.
func VerifyToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return layoutToolHandler(repoRoot, "verify")
}

// InspectToolHandler returns the pf_inspect handler: `platform-factory
// inspect`, summarizing a local OCI layout's manifests and platforms.
func InspectToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return layoutToolHandler(repoRoot, "inspect")
}

func layoutToolHandler(repoRoot, subcommand string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var a layoutArguments
		if err := json.Unmarshal(arguments, &a); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
		}
		root, err := resolveProjectRoot(repoRoot, a.ProjectRoot)
		if err != nil {
			return "", err
		}
		layout, err := scopedRelative(root, a.Layout)
		if err != nil {
			return "", err
		}
		if layout == "" {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "layout is required")
		}
		format := a.Format
		if format == "" {
			format = "json"
		}
		args := []string{"--format", format, layout}
		result, err := run(ctx, root, subcommand, args)
		if err != nil {
			return "", err
		}
		return encode(result)
	}
}

package product

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

type buildArguments struct {
	// Executable is the low-level positional target - a compiled
	// executable to wrap directly. Leave empty for project mode
	// (platform-factory build with no target discovers the nearest
	// pf.yaml the same way running `platform-factory build` from a
	// project directory does).
	Executable       string   `json:"executable"`
	DryRun           bool     `json:"dry_run"`
	Output           string   `json:"output"`
	Config           string   `json:"config"`
	Architecture     string   `json:"arch"`
	OS               string   `json:"os"`
	Platforms        []string `json:"platforms"`
	Entrypoint       string   `json:"entrypoint"`
	Profile          string   `json:"profile"`
	Image            string   `json:"image"`
	Tag              string   `json:"tag"`
	Format           string   `json:"format"`
	Compression      string   `json:"compression"`
	SemanticLayers   bool     `json:"semantic_layers"`
	Rebuild          int      `json:"rebuild"`
	RequireIdentical bool     `json:"require_identical"`
	ExtraFiles       []string `json:"extra_files"`
	Labels           []string `json:"labels"`
	ExtraArgs        []string `json:"extra_args"`
	ProjectRoot      string   `json:"project_root"`
}

// BuildToolHandler returns the pf_build handler: `platform-factory
// build`, either in project mode (nearest pf.yaml) or low-level mode
// (wrap one compiled executable directly).
func BuildToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var a buildArguments
		if len(arguments) > 0 && string(arguments) != "{}" {
			if err := json.Unmarshal(arguments, &a); err != nil {
				return "", toolerror.New(toolerror.ErrInvalidArgument, "invalid arguments: %v", err)
			}
		}
		if err := validExtraArgs(a.ExtraArgs); err != nil {
			return "", err
		}
		root, err := resolveProjectRoot(repoRoot, a.ProjectRoot)
		if err != nil {
			return "", err
		}
		executable, err := scopedRelative(root, a.Executable)
		if err != nil {
			return "", err
		}
		output, err := scopedRelative(root, a.Output)
		if err != nil {
			return "", err
		}
		config, err := scopedRelative(root, a.Config)
		if err != nil {
			return "", err
		}

		var args []string
		args = boolFlag(args, "--dry-run", a.DryRun)
		args = stringFlag(args, "--output", output)
		args = stringFlag(args, "--config", config)
		args = stringFlag(args, "--arch", a.Architecture)
		args = stringFlag(args, "--os", a.OS)
		for _, platform := range a.Platforms {
			args = append(args, "--platform", platform)
		}
		args = stringFlag(args, "--entrypoint", a.Entrypoint)
		args = stringFlag(args, "--profile", a.Profile)
		args = stringFlag(args, "--image", a.Image)
		args = stringFlag(args, "--tag", a.Tag)
		args = stringFlag(args, "--format", a.Format)
		args = stringFlag(args, "--compression", a.Compression)
		args = boolFlag(args, "--semantic-layers", a.SemanticLayers)
		if a.Rebuild > 0 {
			args = append(args, "--rebuild", strconv.Itoa(a.Rebuild))
		}
		args = boolFlag(args, "--require-identical", a.RequireIdentical)
		for _, extraFile := range a.ExtraFiles {
			args = append(args, "--extra-file", extraFile)
		}
		for _, label := range a.Labels {
			args = append(args, "--label", label)
		}
		args = append(args, a.ExtraArgs...)
		if executable != "" {
			args = append(args, executable)
		}

		result, err := run(ctx, root, "build", args)
		if err != nil {
			return "", err
		}
		return encode(result)
	}
}

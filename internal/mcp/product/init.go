package product

import (
	"context"
	"encoding/json"
)

type initArguments struct {
	Directory      string   `json:"directory"`
	DryRun         bool     `json:"dry_run"`
	Yes            bool     `json:"yes"`
	BootDisk       string   `json:"boot_disk"`
	Language       string   `json:"language"`
	Artifact       string   `json:"artifact"`
	DependencyMode string   `json:"dependency_mode"`
	Runtime        string   `json:"runtime"`
	Engine         string   `json:"engine"`
	BuildCommand   string   `json:"build_command"`
	BuildArgs      []string `json:"build_args"`
	ExtractTo      string   `json:"extract_to"`
	ArchiveFormat  string   `json:"archive_format"`
	FilenameStyle  string   `json:"filename_style"`
	ExtraArgs      []string `json:"extra_args"`
}

// InitToolHandler returns the pf_init handler: `platform-factory init`,
// scaffolding a pf.yaml project from a source directory.
func InitToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var a initArguments
		if len(arguments) > 0 && string(arguments) != "{}" {
			if err := json.Unmarshal(arguments, &a); err != nil {
				return "", err
			}
		}
		if err := validExtraArgs(a.ExtraArgs); err != nil {
			return "", err
		}
		directory, err := scopedRelative(repoRoot, a.Directory)
		if err != nil {
			return "", err
		}
		extractTo, err := scopedRelative(repoRoot, a.ExtractTo)
		if err != nil {
			return "", err
		}

		var args []string
		args = boolFlag(args, "--dry-run", a.DryRun)
		args = boolFlag(args, "--yes", a.Yes)
		args = stringFlag(args, "--boot-disk", a.BootDisk)
		args = stringFlag(args, "--language", a.Language)
		args = stringFlag(args, "--artifact", a.Artifact)
		args = stringFlag(args, "--dependency-mode", a.DependencyMode)
		args = stringFlag(args, "--runtime", a.Runtime)
		args = stringFlag(args, "--engine", a.Engine)
		args = stringFlag(args, "--build-command", a.BuildCommand)
		for _, buildArg := range a.BuildArgs {
			args = append(args, "--build-arg", buildArg)
		}
		args = stringFlag(args, "--extract-to", extractTo)
		args = stringFlag(args, "--archive-format", a.ArchiveFormat)
		args = stringFlag(args, "--filename-style", a.FilenameStyle)
		args = append(args, a.ExtraArgs...)
		if directory != "" {
			args = append(args, directory)
		}

		result, err := run(ctx, repoRoot, "init", args)
		if err != nil {
			return "", err
		}
		return encode(result)
	}
}

package product

import (
	"context"
	"encoding/json"
)

type publishArguments struct {
	Layout           string   `json:"layout"`
	Image            string   `json:"image"`
	DryRun           bool     `json:"dry_run"`
	Yes              bool     `json:"yes"`
	PushOnly         bool     `json:"push_only"`
	DeployOnly       bool     `json:"deploy_only"`
	Sign             bool     `json:"sign"`
	SBOM             bool     `json:"sbom"`
	Provenance       string   `json:"provenance"`
	Journal          string   `json:"journal"`
	KeyDir           string   `json:"key_dir"`
	KeyName          string   `json:"key_name"`
	Policy           string   `json:"policy"`
	Evidence         string   `json:"evidence"`
	AllowIncomplete  bool     `json:"allow_incomplete_evidence"`
	SourceRef        string   `json:"source_ref"`
	InsecureRegistry bool     `json:"insecure_registry"`
	MountFrom        string   `json:"mount_from"`
	Format           string   `json:"format"`
	Reports          string   `json:"reports"`
	ExtraArgs        []string `json:"extra_args"`
}

// PublishToolHandler returns the pf_publish handler: `platform-factory
// publish`, pushing a verified OCI layout to a registry with optional
// signature/SBOM/provenance artifacts and a policy gate. Credentials
// are never accepted as a tool argument - the subprocess inherits this
// server's own environment (PLATFORM_FACTORY_REGISTRY_USERNAME/PASSWORD),
// the same convention every other credential in this codebase uses.
func PublishToolHandler(repoRoot string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var a publishArguments
		if len(arguments) > 0 && string(arguments) != "{}" {
			if err := json.Unmarshal(arguments, &a); err != nil {
				return "", err
			}
		}
		if err := validExtraArgs(a.ExtraArgs); err != nil {
			return "", err
		}
		layout, err := scopedRelative(repoRoot, a.Layout)
		if err != nil {
			return "", err
		}
		policy, err := scopedRelative(repoRoot, a.Policy)
		if err != nil {
			return "", err
		}
		evidence, err := scopedRelative(repoRoot, a.Evidence)
		if err != nil {
			return "", err
		}
		reports, err := scopedRelative(repoRoot, a.Reports)
		if err != nil {
			return "", err
		}

		var args []string
		args = boolFlag(args, "--dry-run", a.DryRun)
		args = boolFlag(args, "--yes", a.Yes)
		args = boolFlag(args, "--push-only", a.PushOnly)
		args = boolFlag(args, "--deploy-only", a.DeployOnly)
		args = boolFlag(args, "--sign", a.Sign)
		args = boolFlag(args, "--sbom", a.SBOM)
		args = stringFlag(args, "--provenance", a.Provenance)
		args = stringFlag(args, "--journal", a.Journal)
		args = stringFlag(args, "--key-dir", a.KeyDir)
		args = stringFlag(args, "--key-name", a.KeyName)
		args = stringFlag(args, "--policy", policy)
		args = stringFlag(args, "--evidence", evidence)
		args = boolFlag(args, "--allow-incomplete-evidence", a.AllowIncomplete)
		args = stringFlag(args, "--source-ref", a.SourceRef)
		args = boolFlag(args, "--insecure-registry", a.InsecureRegistry)
		args = stringFlag(args, "--mount-from", a.MountFrom)
		args = stringFlag(args, "--format", a.Format)
		args = stringFlag(args, "--reports", reports)
		args = append(args, a.ExtraArgs...)
		if layout != "" {
			args = append(args, layout)
		}
		if a.Image != "" {
			args = append(args, a.Image)
		}

		result, err := run(ctx, repoRoot, "publish", args)
		if err != nil {
			return "", err
		}
		return encode(result)
	}
}

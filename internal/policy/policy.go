// Package policy evaluates the minimal, versioned native publication policy.
package policy

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/CYPT71/platform-factory/internal/core"
	typederrors "github.com/CYPT71/platform-factory/internal/errors"
	"github.com/CYPT71/platform-factory/internal/strictjson"
)

const (
	APIVersion = "platform-factory.dev/policy/v1"
	// LegacyAPIVersion is the pre-rebrand identifier, still accepted for
	// the documented compatibility overlap window (see
	// docs/api-compatibility.md) - a policy.json a deployment already
	// has on disk may not have been regenerated yet.
	LegacyAPIVersion = "secure-oci.dev/policy/v1"
)

type Evidence struct {
	SubjectDigest       string `json:"subject_digest"`
	SourcesPinned       bool   `json:"sources_pinned"`
	BasePinned          bool   `json:"base_pinned"`
	ToolchainPinned     bool   `json:"toolchain_pinned"`
	PluginsPinned       bool   `json:"plugins_pinned"`
	NonRoot             bool   `json:"non_root"`
	ReadOnlyRootFS      bool   `json:"read_only_rootfs"`
	CapabilitiesDropped bool   `json:"capabilities_dropped"`
	SecretsAbsent       bool   `json:"secrets_absent"`
	SBOM                bool   `json:"sbom"`
	Provenance          bool   `json:"provenance"`
	Signature           bool   `json:"signature"`
	Reproducible        bool   `json:"reproducible"`
}

type Rules struct {
	APIVersion          string `json:"api_version"`
	RequirePins         bool   `json:"require_pins"`
	RequireHardening    bool   `json:"require_hardening"`
	RequireSBOM         bool   `json:"require_sbom"`
	RequireProvenance   bool   `json:"require_provenance"`
	RequireSignature    bool   `json:"require_signature"`
	RequireReproducible bool   `json:"require_reproducible"`
}

type Decision struct {
	Allowed bool     `json:"allowed"`
	Reasons []string `json:"reasons,omitempty"`
}

func Evaluate(rules Rules, evidence Evidence) (Decision, error) {
	if rules.APIVersion != APIVersion && rules.APIVersion != LegacyAPIVersion {
		return Decision{}, typederrors.Newf(typederrors.CodePolicyEvaluation,
			"policy: unsupported api_version %q (want %q)", rules.APIVersion, APIVersion)
	}
	var reasons []string
	if evidence.SubjectDigest == "" {
		reasons = append(reasons, "subject digest is missing")
	}
	if rules.RequirePins && (!evidence.SourcesPinned || !evidence.BasePinned || !evidence.ToolchainPinned || !evidence.PluginsPinned) {
		reasons = append(reasons, "sources, base, toolchain and plugins must be pinned")
	}
	if rules.RequireHardening && (!evidence.NonRoot || !evidence.ReadOnlyRootFS || !evidence.CapabilitiesDropped || !evidence.SecretsAbsent) {
		reasons = append(reasons, "runtime hardening evidence is incomplete")
	}
	if rules.RequireSBOM && !evidence.SBOM {
		reasons = append(reasons, "SBOM is required")
	}
	if rules.RequireProvenance && !evidence.Provenance {
		reasons = append(reasons, "provenance is required")
	}
	if rules.RequireSignature && !evidence.Signature {
		reasons = append(reasons, "signature is required")
	}
	if rules.RequireReproducible && !evidence.Reproducible {
		reasons = append(reasons, "reproducibility proof is required")
	}
	sort.Strings(reasons)
	return Decision{Allowed: len(reasons) == 0, Reasons: reasons}, nil
}

// DecodeRulesAndEvidence reads and strictly decodes the Rules at
// policyPath and the Evidence at evidencePath - the canonical "load
// what Evaluate needs from disk" pair every `--policy`/`--evidence`
// flag combination in this CLI uses. evidencePath is required
// whenever policyPath is meaningful: a policy with no evidence to
// evaluate it against is never a valid combination.
func DecodeRulesAndEvidence(policyPath, evidencePath string) (Rules, Evidence, error) {
	if evidencePath == "" {
		return Rules{}, Evidence{}, errors.New("--evidence is required with --policy")
	}
	var rules Rules
	if err := strictjson.DecodeFile(policyPath, &rules); err != nil {
		return Rules{}, Evidence{}, fmt.Errorf("decode rules: %w", err)
	}
	var evidence Evidence
	if err := strictjson.DecodeFile(evidencePath, &evidence); err != nil {
		return Rules{}, Evidence{}, fmt.Errorf("decode evidence: %w", err)
	}
	return rules, evidence, nil
}

// DerivePipelineEvidence computes static policy facts from validated pipeline
// content and verified plugin digests. Manifest parsing, trust verification,
// and conversion belong to the composition boundary; policy only consumes the
// verified identity that crosses into the domain.
func DerivePipelineEvidence(definition core.Pipeline, pluginDigests []string) Evidence {
	evidence := Evidence{
		SourcesPinned: true, BasePinned: true, ToolchainPinned: true,
		PluginsPinned: true, NonRoot: true, ReadOnlyRootFS: true,
		CapabilitiesDropped: true, SecretsAbsent: true,
	}
	for _, input := range definition.Inputs {
		evidence.SourcesPinned = evidence.SourcesPinned && validSHA256(input.Digest)
	}
	for _, stage := range definition.Stages {
		// In the language-neutral API Stage.Base is the complete execution
		// environment/toolchain. A stage without one uses an unpinned host
		// toolchain and therefore fails both facts.
		pinned := stage.Base != nil && validSHA256(stage.Base.Digest)
		evidence.BasePinned = evidence.BasePinned && pinned
		evidence.ToolchainPinned = evidence.ToolchainPinned && pinned
		evidence.NonRoot = evidence.NonRoot && stage.Sandbox.NonRoot
		evidence.ReadOnlyRootFS = evidence.ReadOnlyRootFS && stage.Sandbox.ReadOnlyRoot
	}
	for _, digest := range pluginDigests {
		evidence.PluginsPinned = evidence.PluginsPinned && validSHA256(digest)
	}
	return evidence
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

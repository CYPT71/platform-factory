package policy

import (
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/core"
	typederrors "github.com/CYPT71/secure-oci-base/internal/errors"
)

func TestEvaluateRejectsUnsupportedAPIVersionAsTypedError(t *testing.T) {
	_, err := Evaluate(Rules{APIVersion: "secure-oci.dev/policy/v0"}, Evidence{SubjectDigest: "sha256:abc"})
	if err == nil {
		t.Fatal("expected an error for an unsupported api_version")
	}
	if !typederrors.HasCode(err, typederrors.CodePolicyEvaluation) {
		t.Fatalf("expected code %q, got err=%v code=%q", typederrors.CodePolicyEvaluation, err, typederrors.GetErrorCode(err))
	}
}

func TestEvaluateFailsClosedWithReasons(t *testing.T) {
	rules := Rules{
		APIVersion: APIVersion, RequirePins: true, RequireHardening: true,
		RequireSBOM: true, RequireProvenance: true, RequireSignature: true, RequireReproducible: true,
	}
	decision, err := Evaluate(rules, Evidence{SubjectDigest: "sha256:abc"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || len(decision.Reasons) != 6 {
		t.Fatalf("decision=%+v", decision)
	}
	evidence := Evidence{
		SubjectDigest: "sha256:abc", SourcesPinned: true, BasePinned: true, ToolchainPinned: true, PluginsPinned: true,
		NonRoot: true, ReadOnlyRootFS: true, CapabilitiesDropped: true, SecretsAbsent: true,
		SBOM: true, Provenance: true, Signature: true, Reproducible: true,
	}
	decision, err = Evaluate(rules, evidence)
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

func TestDerivePipelineEvidenceRejectsHostToolchain(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	definition := core.Pipeline{
		Inputs: []core.Input{{ID: "source", Digest: digest}},
		Stages: []core.Stage{{
			ID: "build", Base: &core.ImageReference{Digest: digest},
			Sandbox: core.SandboxPolicy{NonRoot: true, ReadOnlyRoot: true},
		}},
	}
	evidence := DerivePipelineEvidence(definition, nil)
	if !evidence.SourcesPinned || !evidence.BasePinned || !evidence.ToolchainPinned ||
		!evidence.NonRoot || !evidence.ReadOnlyRootFS {
		t.Fatalf("evidence=%+v", evidence)
	}
	definition.Stages[0].Base = nil
	evidence = DerivePipelineEvidence(definition, nil)
	if evidence.BasePinned || evidence.ToolchainPinned {
		t.Fatalf("host toolchain was considered pinned: %+v", evidence)
	}
}

func TestDerivePipelineEvidenceRequiresCanonicalVerifiedPluginDigests(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	evidence := DerivePipelineEvidence(core.Pipeline{}, []string{digest})
	if !evidence.PluginsPinned {
		t.Fatalf("canonical digest was rejected: %+v", evidence)
	}
	evidence = DerivePipelineEvidence(core.Pipeline{}, []string{"plugin-name:latest"})
	if evidence.PluginsPinned {
		t.Fatalf("mutable plugin reference was accepted: %+v", evidence)
	}
}

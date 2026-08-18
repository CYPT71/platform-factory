package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/policy"
	"github.com/CYPT71/platform-factory/internal/project"
)

func TestWriteLaunchPublicationEvidenceWritesAllThreeDocuments(t *testing.T) {
	root := t.TempDir()
	loaded := project.Loaded{
		File:   filepath.Join(root, "platform-factory.yaml"),
		Root:   root,
		Config: project.Config{Platform: "linux/amd64"},
	}
	policyPath := filepath.Join(root, "policy.json")
	evidencePath := filepath.Join(root, "evidence.json")
	provenancePath := filepath.Join(root, "provenance.json")

	if err := WriteLaunchPublicationEvidence(policyPath, evidencePath, provenancePath,
		loaded, "sha256:abc", "1.5.0"); err != nil {
		t.Fatal(err)
	}

	var rules policy.Rules
	readJSON(t, policyPath, &rules)
	if rules.APIVersion != policy.APIVersion || !rules.RequireHardening || !rules.RequireSBOM ||
		!rules.RequireProvenance || !rules.RequireSignature || !rules.RequireReproducible {
		t.Fatalf("rules=%+v, want every requirement set for the publish lifecycle", rules)
	}

	var evidence policy.Evidence
	readJSON(t, evidencePath, &evidence)
	if !evidence.NonRoot || !evidence.ReadOnlyRootFS || !evidence.CapabilitiesDropped ||
		!evidence.SecretsAbsent || !evidence.Reproducible {
		t.Fatalf("evidence=%+v, want every precondition asserted", evidence)
	}

	var provenance map[string]any
	readJSON(t, provenancePath, &provenance)
	if provenance["builder"] != "platform-factory/1.5.0" || provenance["output"] != "sha256:abc" ||
		provenance["config"] != "platform-factory.yaml" || provenance["platform"] != "linux/amd64" ||
		provenance["reproducible"] != true {
		t.Fatalf("provenance=%v", provenance)
	}

	// WriteLaunchPublicationEvidence only asserts the hardening/
	// reproducibility preconditions that are true unconditionally on this
	// lifecycle path; SBOM/provenance/signature facts are overlaid by the
	// caller once those artifacts actually exist (mirroring publish's
	// EvaluatePolicy pattern), so on its own this evidence should fail
	// closed on exactly those three, not on hardening or reproducibility.
	evidence.SubjectDigest = "sha256:abc"
	decision, err := policy.Evaluate(rules, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed {
		t.Fatalf("decision=%+v, want denial before SBOM/provenance/signature are overlaid", decision)
	}
	for _, unwanted := range []string{"hardening", "reproducib", "pinned"} {
		for _, reason := range decision.Reasons {
			if strings.Contains(reason, unwanted) {
				t.Fatalf("reasons=%v should not mention %q: every hardening/reproducibility precondition is set", decision.Reasons, unwanted)
			}
		}
	}

	evidence.SBOM, evidence.Provenance, evidence.Signature = true, true, true
	decision, err = policy.Evaluate(rules, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed {
		t.Fatalf("decision=%+v, want allowed once SBOM/provenance/signature are overlaid", decision)
	}
}

func TestWriteLaunchPublicationEvidencePropagatesWriteFailure(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded := project.Loaded{File: filepath.Join(root, "platform-factory.yaml"), Root: root}
	// policyPath's parent is a regular file, so os.MkdirAll must fail.
	policyPath := filepath.Join(blocked, "sub", "policy.json")
	evidencePath := filepath.Join(root, "evidence.json")
	provenancePath := filepath.Join(root, "provenance.json")

	if err := WriteLaunchPublicationEvidence(policyPath, evidencePath, provenancePath,
		loaded, "sha256:abc", "1.0.0"); err == nil {
		t.Fatal("expected an error when policyPath's parent directory cannot be created")
	}
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/attestation"
	"github.com/CYPT71/platform-factory/internal/policy"
	"github.com/CYPT71/platform-factory/internal/signing"
)

func TestReproducibleBuildEmitsVerifiedSignedReleaseEvidence(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "app")
	dist := filepath.Join(root, "release")
	reports := filepath.Join(dist, "reports")
	keyDir := filepath.Join(root, "keys")
	if err := os.WriteFile(binary, []byte("deterministic fixture payload"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := signing.NewFileKeyStore(keyDir)
	if err != nil {
		t.Fatal(err)
	}
	public, err := store.PublicKey("release")
	if err != nil {
		t.Fatal(err)
	}
	rules := policy.Rules{APIVersion: policy.APIVersion, RequireSBOM: true, RequireProvenance: true, RequireSignature: true, RequireReproducible: true}
	policyData, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(root, "policy.json")
	if err := os.WriteFile(policyPath, policyData, 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	args := []string{"build", "--yes", "--dist", dist, "--reports", reports, "--rebuild", "2", "--require-identical", "--sign-key-dir", keyDir, "--policy", policyPath, binary}
	if code := run(args, &out, &diagnostics); code != 0 {
		t.Fatalf("code=%d: %s", code, &diagnostics)
	}
	var result struct {
		Digest       string `json:"digest"`
		Reproducible bool   `json:"reproducible"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Reproducible || result.Digest == "" {
		t.Fatalf("result=%s", &out)
	}
	for _, name := range []string{"sbom.json", "provenance.json", "reports/reproducibility.json"} {
		if _, err := os.Stat(filepath.Join(dist, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"attestations/provenance.dsse.json", "signatures/subject.dsse.json"} {
		raw, err := os.ReadFile(filepath.Join(dist, name))
		if err != nil {
			t.Fatal(err)
		}
		var envelope attestation.Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.Signatures) != 1 {
			t.Fatal("missing signature", name)
		}
		payload, err := attestation.Verify(envelope, map[string]ed25519.PublicKey{envelope.Signatures[0].KeyID: public})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(payload, []byte(result.Digest)) {
			t.Fatalf("signature does not bind build digest: %s", payload)
		}
	}
	raw, err := os.ReadFile(filepath.Join(reports, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence policy.Evidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	decision, err := policy.Evaluate(rules, evidence)
	if err != nil || !decision.Allowed || evidence.SubjectDigest != result.Digest {
		t.Fatalf("evidence=%+v decision=%+v err=%v", evidence, decision, err)
	}
}

func TestReproducibleBuildFailsIfRequestedSigningFails(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "app")
	keys := filepath.Join(root, "keys")
	if err := os.WriteFile(binary, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(keys, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keys, "release.key"), []byte("invalid private key"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	code := run([]string{"build", "--yes", "--dist", filepath.Join(root, "release"), "--rebuild", "2", "--require-identical", "--sign-key-dir", keys, binary}, &out, &diagnostics)
	if code != 1 || !bytes.Contains(diagnostics.Bytes(), []byte("release evidence")) {
		t.Fatalf("code=%d stderr=%s", code, &diagnostics)
	}
	if bytes.Contains(out.Bytes(), []byte(`"valid": true`)) {
		t.Fatal("reported success after signing failed")
	}
}

func TestMutationOperationIDsSeparateNewRequestsAndPreserveRetryKey(t *testing.T) {
	first, err := mutationOperationID("deploy-apply", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := mutationOperationID("deploy-apply", "")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("new deployment requests shared an operation ID")
	}
	retry, err := mutationOperationID("deploy-apply", string(first))
	if err != nil {
		t.Fatal(err)
	}
	if retry != first {
		t.Fatal("returned operation ID cannot be retried verbatim")
	}
	for _, invalid := range []string{"../escape", strings.Repeat("a", 65)} {
		if _, err := mutationOperationID("rollback", invalid); err == nil {
			t.Fatal("accepted unsafe retry key")
		}
	}
}

func TestDeployAndRollbackRejectUnsafeRetryKeysBeforeExecution(t *testing.T) {
	for _, command := range []string{"deploy", "rollback"} {
		var out, diagnostics bytes.Buffer
		args := []string{"--yes", "--operation-id", "../unsafe"}
		code := 0
		if command == "deploy" {
			code = runDeploy(context.Background(), args, &out, &diagnostics, nil)
		} else {
			code = runRollback(context.Background(), args, &out, &diagnostics, nil)
		}
		if code != 2 {
			t.Fatalf("%s accepted invalid operation-id: code=%d %s", command, code, &diagnostics)
		}
	}
}

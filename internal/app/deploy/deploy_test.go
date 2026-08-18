package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluatePolicyFailsClosed(t *testing.T) {
	root := t.TempDir()
	rules := filepath.Join(root, "policy.json")
	evidence := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(rules, []byte(`{"api_version":"platform-factory.dev/policy/v1","require_signature":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte(`{"signature":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	decision, err := EvaluatePolicy(rules, evidence, digest)
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}

	if err := os.WriteFile(evidence, []byte(`{"signature":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	decision, err = EvaluatePolicy(rules, evidence, digest)
	if err != nil || decision.Allowed {
		t.Fatalf("expected denial for missing signature evidence, decision=%+v err=%v", decision, err)
	}
}

func TestEvaluatePolicyDecodeFailures(t *testing.T) {
	if _, err := EvaluatePolicy("policy.json", "", "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("expected empty evidence path rejection")
	}
	root := t.TempDir()
	evidencePath := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(evidencePath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluatePolicy(filepath.Join(root, "missing-policy.json"), evidencePath, "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("expected missing policy file rejection")
	}
}

func TestParseKubernetesExtensions(t *testing.T) {
	configs, secrets, volumes, err := ParseKubernetesExtensions(
		[]string{"MODE=production"}, []string{"DATABASE_PASSWORD=database/password"}, []string{"/var/lib/api=20Gi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Key != "MODE" || configs[0].Value != "production" {
		t.Fatalf("configs=%+v", configs)
	}
	if len(secrets) != 1 || secrets[0].Env != "DATABASE_PASSWORD" || secrets[0].Secret != "database" || secrets[0].Key != "password" {
		t.Fatalf("secrets=%+v", secrets)
	}
	if len(volumes) != 1 || volumes[0].MountPath != "/var/lib/api" || volumes[0].Size != "20Gi" {
		t.Fatalf("volumes=%+v", volumes)
	}
}

func TestParseKubernetesExtensionsRejectsMalformedValues(t *testing.T) {
	if _, _, _, err := ParseKubernetesExtensions([]string{"MISSING"}, nil, nil); err == nil {
		t.Fatal("expected --config without = to be rejected")
	}
	if _, _, _, err := ParseKubernetesExtensions(nil, []string{"TOKEN=secret"}, nil); err == nil {
		t.Fatal("expected --secret-env without SECRET/KEY to be rejected")
	}
	if _, _, _, err := ParseKubernetesExtensions(nil, []string{"TOKEN=secret/a/b"}, nil); err == nil {
		t.Fatal("expected --secret-env with an extra slash in the key to be rejected")
	}
	if _, _, _, err := ParseKubernetesExtensions(nil, nil, []string{"no-equals-sign"}); err == nil {
		t.Fatal("expected --volume without = to be rejected")
	}
}

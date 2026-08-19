package product

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestPublishToolHandlerRejectsMalformedJSON(t *testing.T) {
	_, err := PublishToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestPublishToolHandlerRejectsNULByteInExtraArgs(t *testing.T) {
	payload := json.RawMessage("{\"extra_args\":[\"a\\u0000b\"]}")
	_, err := PublishToolHandler(t.TempDir())(context.Background(), payload)
	if err == nil {
		t.Fatal("expected an error for a NUL byte in extra_args")
	}
}

func TestPublishToolHandlerRejectsPathTraversalInPolicy(t *testing.T) {
	_, err := PublishToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"policy":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal policy")
	}
}

func TestPublishToolHandlerRejectsPathTraversalInEvidence(t *testing.T) {
	_, err := PublishToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"evidence":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal evidence directory")
	}
}

func TestPublishToolHandlerRejectsPathTraversalInReports(t *testing.T) {
	_, err := PublishToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"reports":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal reports directory")
	}
}

// TestPublishToolHandlerBuildsFullArgs exercises every flag
// publishArguments understands, in the exact order publish.go appends
// them, via the helper-process stand-in so the assertion is against the
// real argv run() hands to the subprocess - including that both layout
// and image, when both set, are appended layout-then-image.
func TestPublishToolHandlerBuildsFullArgs(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	payload := `{
		"layout": "oci",
		"image": "img:tag",
		"dry_run": true,
		"yes": true,
		"push_only": true,
		"deploy_only": true,
		"sign": true,
		"sbom": true,
		"provenance": "prov.json",
		"journal": "journal.json",
		"key_dir": "keys",
		"key_name": "release",
		"policy": "policy.yaml",
		"evidence": "evidence",
		"allow_incomplete_evidence": true,
		"source_ref": "sha256:deadbeef",
		"insecure_registry": true,
		"mount_from": "base",
		"format": "text",
		"reports": "reports",
		"extra_args": ["--z"]
	}`
	out, err := PublishToolHandler(repoRoot)(context.Background(), json.RawMessage(payload))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON: %v\n%s", err, out)
	}
	if result.Command != "publish" {
		t.Fatalf("command = %q, want %q", result.Command, "publish")
	}
	want := []string{
		"--dry-run", "--yes", "--push-only", "--deploy-only", "--sign", "--sbom",
		"--provenance", "prov.json", "--journal", "journal.json",
		"--key-dir", "keys", "--key-name", "release",
		"--policy", "policy.yaml", "--evidence", "evidence",
		"--allow-incomplete-evidence", "--source-ref", "sha256:deadbeef",
		"--insecure-registry", "--mount-from", "base",
		"--format", "text", "--reports", "reports",
		"--z", "oci", "img:tag",
	}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

func TestPublishToolHandlerOmitsUnsetFlags(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := PublishToolHandler(repoRoot)(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Args) != 0 {
		t.Fatalf("expected no args for an all-default payload, got %v", result.Args)
	}
}

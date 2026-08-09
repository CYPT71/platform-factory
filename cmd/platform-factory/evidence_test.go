package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunEvidenceDerivesPinsAndHardening(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	pipeline := `{
	  "api_version":"secure-oci.dev/v1beta1",
	  "name":"release",
	  "inputs":[{"id":"source","kind":"directory","source":".","digest":"` + digest + `"}],
	  "stages":[{
	    "id":"build",
	    "base":{"reference":"registry.example/toolchain","digest":"` + digest + `","platform":"linux/amd64"},
	    "command":{"executable":"/bin/build"},
	    "sandbox":{"read_only_root":true,"non_root":true}
	  }]
	}`
	filename := filepath.Join(t.TempDir(), "pipeline.json")
	if err := os.WriteFile(filename, []byte(pipeline), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runEvidence([]string{"--reproducible", filename}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, field := range []string{
		`"sources_pinned": true`, `"toolchain_pinned": true`,
		`"non_root": true`, `"reproducible": true`,
	} {
		if !strings.Contains(stdout.String(), field) {
			t.Fatalf("missing %s: %s", field, stdout.String())
		}
	}
}

func TestRunEvidenceFailsClosedForInvalidOrUnpinnedInputs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{{"--help"}, nil, {"--unknown"}} {
		stdout.Reset()
		stderr.Reset()
		code := runEvidence(args, &stdout, &stderr)
		if len(args) == 1 && args[0] == "--help" {
			if code != 0 {
				t.Fatalf("help code=%d", code)
			}
		} else if code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
	root := t.TempDir()
	invalid := filepath.Join(root, "invalid.json")
	if err := os.WriteFile(invalid, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runEvidence([]string{invalid}, &stdout, &stderr); code != 1 {
		t.Fatalf("invalid pipeline code=%d", code)
	}
	unpinned := filepath.Join(root, "unpinned.json")
	if err := os.WriteFile(unpinned, []byte(`{
	  "api_version":"secure-oci.dev/v1beta1",
	  "name":"unpinned",
	  "stages":[{"id":"build","command":{"executable":"/bin/build"}}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runEvidence([]string{unpinned}, &stdout, &stderr); code != 1 {
		t.Fatalf("unpinned pipeline code=%d", code)
	}
	if code := runEvidence([]string{"--plugin-dir", filepath.Join(root, "missing"), unpinned},
		&stdout, &stderr); code != 1 {
		t.Fatalf("missing plugin directory code=%d", code)
	}
}

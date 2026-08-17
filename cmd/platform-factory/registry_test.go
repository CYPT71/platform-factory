package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/registry"
)

func TestRunRegistryInspectRequiresAndVerifiesImmutableDigest(t *testing.T) {
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("0", 64) + `","size":2},"layers":[]}`)
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	host := "registry.example"
	original := inspectRegistryManifest
	inspectRegistryManifest = func(_ context.Context, target registry.Reference, requested, scheme, _, _ string) ([]byte, string, error) {
		if target.Registry != host || target.Repository != "team/service" || scheme != "http" {
			t.Fatalf("target=%+v scheme=%s", target, scheme)
		}
		if requested != digest {
			return nil, "", fmt.Errorf("registry: manifest %s digest mismatch", requested)
		}
		return body, "application/vnd.oci.image.manifest.v1+json", nil
	}
	t.Cleanup(func() { inspectRegistryManifest = original })
	var stdout, stderr bytes.Buffer
	if code := runRegistry([]string{"inspect", "--scheme", "http", host + "/team/service@" + digest}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"valid": true`) || !strings.Contains(stdout.String(), digest) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runRegistry([]string{"inspect", "--scheme", "http", host + "/team/service:latest"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "@sha256") {
		t.Fatalf("mutable tag code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	wrong := "sha256:" + strings.Repeat("0", 64)
	if code := runRegistry([]string{"inspect", "--scheme", "http", fmt.Sprintf("%s/team/service@%s", host, wrong)}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "digest mismatch") {
		t.Fatalf("mismatch code=%d stderr=%s", code, stderr.String())
	}
}

package dockerarchive

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func dockerTar(t *testing.T, manifest any, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	tw := tar.NewWriter(&output)
	manifestBytes, _ := json.Marshal(manifest)
	files["manifest.json"] = string(manifestBytes)
	for name, data := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(data))})
		_, _ = tw.Write([]byte(data))
	}
	_ = tw.Close()
	return output.Bytes()
}

func TestVerifyRejectsMissingTraversalAndDigestMismatch(t *testing.T) {
	if _, err := Verify(context.Background(), bytes.NewReader(dockerTar(t, []map[string]any{{"Config": "config.json", "Layers": []string{"layer.tar"}}}, map[string]string{"../unreferenced": "x", "config.json": "{}", "layer.tar": "x"}))); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unreferenced traversal header err=%v", err)
	}
	manifest := []map[string]any{{"Config": "missing.json", "RepoTags": []string{"x:v1"}, "Layers": []string{"layer.tar"}}}
	if _, err := Verify(context.Background(), bytes.NewReader(dockerTar(t, manifest, map[string]string{"layer.tar": "x"}))); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing reference err=%v", err)
	}
	manifest[0]["Config"] = "../config"
	if _, err := Verify(context.Background(), bytes.NewReader(dockerTar(t, manifest, map[string]string{"layer.tar": "x"}))); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("traversal err=%v", err)
	}
	badDigest := strings.Repeat("0", 64) + ".json"
	manifest[0]["Config"] = badDigest
	if _, err := Verify(context.Background(), bytes.NewReader(dockerTar(t, manifest, map[string]string{badDigest: "config", "layer.tar": "x"}))); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("digest mismatch err=%v", err)
	}
}

package layout

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

func gzipPayload(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write(data)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestVerifyLayerRejectsDecompressionBomb(t *testing.T) {
	compressed := gzipPayload(t, bytes.Repeat([]byte{0}, int(maxLayerBytes+1)))
	if err := verifyLayer(compressed, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("decompression bomb accepted")
	}
}
func TestVerifyLayerRejectsConcatenatedAndTrailingStreams(t *testing.T) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	_ = tw.WriteHeader(&tar.Header{Name: "x", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	base := gzipPayload(t, raw.Bytes())
	for _, data := range [][]byte{append(append([]byte(nil), base...), base...), append(append([]byte(nil), base...), []byte("junk")...)} {
		if err := verifyLayer(data, "sha256:"+strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "trailing") {
			t.Fatalf("err=%v", err)
		}
	}
}

package layout

import (
	"bytes"
	"compress/gzip"
	"testing"
)

// FuzzVerifyLayer targets the gzip decompression and tar path-safety
// checks directly (white-box, same package) - reaching this through the
// full on-disk Verify path would need forging a matching SHA-256 blob
// filename per iteration, the same reason FuzzVerifyIndex in
// fuzz_test.go only reaches index.json. A layer blob is exactly the
// untrusted-by-construction content this checks: gzip decompression
// bombs, malformed tar headers, and unsafe paths (absolute, "..",
// symlink-like) must all fail closed, never panic.
func FuzzVerifyLayer(f *testing.F) {
	var validGzip bytes.Buffer
	writer := gzip.NewWriter(&validGzip)
	_, _ = writer.Write([]byte("not a real tar, but valid gzip"))
	_ = writer.Close()
	f.Add(validGzip.Bytes(), "sha256:0000000000000000000000000000000000000000000000000000000000000000"[:71])
	f.Add([]byte("not gzip at all"), "sha256:0000000000000000000000000000000000000000000000000000000000000000"[:71])
	f.Add([]byte{}, "")
	f.Add([]byte{0x1f, 0x8b}, "sha256:0000000000000000000000000000000000000000000000000000000000000000"[:71]) // gzip magic only, truncated

	f.Fuzz(func(t *testing.T, data []byte, diffID string) {
		if len(data) > 1<<20 || len(diffID) > 256 {
			t.Skip()
		}
		_ = verifyLayer(data, diffID)
	})
}

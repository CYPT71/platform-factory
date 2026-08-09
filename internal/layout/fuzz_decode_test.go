package layout

import (
	"encoding/json"
	"testing"
)

// FuzzDecodeManifest and FuzzDecodeConfig target the per-platform manifest
// and image config JSON schemas directly (white-box, same package),
// complementing FuzzVerifyIndex in fuzz_test.go which drives the full
// on-disk Verify path but can only reach index.json - descriptor content
// underneath it is digest-addressed, so reaching arbitrary manifest/config
// bytes through Verify itself would require forging a matching SHA-256
// blob filename per iteration. Decoding this JSON is exactly what a
// pulled-but-not-yet-digest-checked manifest (internal/registry.GetManifest
// verifies the whole-document digest, not field-level structure) or a
// hand-crafted layout would present.

func FuzzDecodeManifest(f *testing.F) {
	f.Add([]byte(`{"schemaVersion":2,"config":{"mediaType":"x","digest":"sha256:00","size":1},"layers":[{"mediaType":"x","digest":"sha256:00","size":1}]}`))
	f.Add([]byte(`{"schemaVersion":1}`))
	f.Add([]byte(`{"schemaVersion":2,"layers":[]}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var document manifest
		_ = json.Unmarshal(data, &document)
	})
}

func FuzzDecodeConfig(f *testing.F) {
	f.Add([]byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":["sha256:00"]},"config":{"User":"65532:65532"}}`))
	f.Add([]byte(`{"rootfs":{"diff_ids":[]}}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var config imageConfig
		_ = json.Unmarshal(data, &config)
	})
}

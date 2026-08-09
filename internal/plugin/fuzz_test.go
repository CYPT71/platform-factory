package plugin

import (
	"encoding/json"
	"testing"
)

// FuzzManifestValidate feeds arbitrary plugin.json bytes at Manifest's
// JSON decode and Validate. A plugin manifest is attacker-controlled by
// construction (a third-party plugin author ships it, T21 in the Threat
// Model), and it's the first thing secure-oci parses before deciding
// whether to trust and execute anything.
func FuzzManifestValidate(f *testing.F) {
	f.Add(`{"name":"demo","version":"1.0.0","executable":"plugin","digest":"sha256:` + fuzzHexDigest + `","capabilities":["detect"]}`)
	f.Add(`{}`)
	f.Add(`null`)
	f.Add(`{"name":"","executable":"../../escape"}`)
	f.Add(`{"digest":"not-a-digest"}`)

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1<<16 {
			t.Skip()
		}
		var manifest Manifest
		if json.Unmarshal([]byte(raw), &manifest) != nil {
			return
		}
		_ = manifest.Validate()
		_, _ = manifest.SigningBytes()
	})
}

const fuzzHexDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"

package marketplace

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func validManifestYAML() string {
	return "api_version: " + ManifestAPIVersion + "\n" +
		"name: acme-runtime\n" +
		"version: v1.0.0\n" +
		"description: Acme runtime plugin\n" +
		"author: Acme Corp\n" +
		"tags: [runtime, acme]\n" +
		"entrypoint: bin/plugin\n" +
		"compatibility: [\">=v1.0.0\", \"<v3.0.0\"]\n"
}

func TestDecodeManifestAcceptsValidDocument(t *testing.T) {
	manifest, err := DecodeManifest(strings.NewReader(validManifestYAML()))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "acme-runtime" || manifest.Version != "v1.0.0" || manifest.Entrypoint != "bin/plugin" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
}

func TestDecodeManifestRejectsUnknownFields(t *testing.T) {
	yaml := validManifestYAML() + "extra_field: surprise\n"
	if _, err := DecodeManifest(strings.NewReader(yaml)); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestDecodeManifestRejectsTrailingDocument(t *testing.T) {
	yaml := validManifestYAML() + "---\napi_version: x\n"
	if _, err := DecodeManifest(strings.NewReader(yaml)); err == nil {
		t.Fatal("expected an error for a second document")
	}
}

func TestValidateRejectsBadFields(t *testing.T) {
	base := func() Manifest {
		m, err := DecodeManifest(strings.NewReader(validManifestYAML()))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	tests := map[string]func(Manifest) Manifest{
		"bad api version":     func(m Manifest) Manifest { m.APIVersion = "wrong"; return m },
		"bad name":            func(m Manifest) Manifest { m.Name = "Not_Valid!"; return m },
		"bad version":         func(m Manifest) Manifest { m.Version = "not-semver"; return m },
		"empty entrypoint":    func(m Manifest) Manifest { m.Entrypoint = ""; return m },
		"absolute entrypoint": func(m Manifest) Manifest { m.Entrypoint = "/etc/passwd"; return m },
		"escaping entrypoint": func(m Manifest) Manifest { m.Entrypoint = "../outside"; return m },
		"bad compatibility":   func(m Manifest) Manifest { m.Compatibility = []string{"not-a-constraint"}; return m },
		"duplicate tag":       func(m Manifest) Manifest { m.Tags = []string{"a", "a"}; return m },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := mutate(base()).Validate(); err == nil {
				t.Fatalf("%s: expected validation error", name)
			}
		})
	}
}

func TestSignAndVerifySignatureRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeManifest(strings.NewReader(validManifestYAML()))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Sign(priv, "key-1")
	if err := manifest.VerifySignature([]ed25519.PublicKey{pub}); err != nil {
		t.Fatalf("signature should verify: %v", err)
	}

	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.VerifySignature([]ed25519.PublicKey{otherPub}); err == nil {
		t.Fatal("signature should not verify against an untrusted key")
	}

	// Tampering with a signed field must invalidate the signature.
	tampered := manifest
	tampered.Entrypoint = "bin/evil"
	if err := tampered.VerifySignature([]ed25519.PublicKey{pub}); err == nil {
		t.Fatal("signature should not verify after the manifest was tampered with")
	}
	tampered = manifest
	tampered.Author = "Impostor"
	if err := tampered.VerifySignature([]ed25519.PublicKey{pub}); err == nil {
		t.Fatal("signature must cover publisher metadata")
	}
}

func TestSigningBytesCanonicalizePermissionSets(t *testing.T) {
	left := Manifest{Permissions: Permissions{Network: []string{"b", "a"}, Filesystem: []string{"z", "x"}}}
	right := Manifest{Permissions: Permissions{Network: []string{"a", "b"}, Filesystem: []string{"x", "z"}}}
	if string(left.SigningBytes()) != string(right.SigningBytes()) {
		t.Fatal("permission order must not change canonical signing bytes")
	}
}

func TestCompatibleWith(t *testing.T) {
	manifest := Manifest{Compatibility: []string{">=v1.0.0", "<v3.0.0"}}
	tests := []struct {
		host string
		want bool
	}{
		{"v1.0.0", true},
		{"v2.5.0", true},
		{"v0.9.0", false},
		{"v3.0.0", false},
		{"1.5.0", true}, // "v" prefix optional on the host side too
	}
	for _, test := range tests {
		got, err := manifest.CompatibleWith(test.host)
		if err != nil {
			t.Fatalf("host=%s: %v", test.host, err)
		}
		if got != test.want {
			t.Errorf("host=%s: got %v, want %v", test.host, got, test.want)
		}
	}
}

func TestCompatibleWithNoConstraintsAcceptsEveryHost(t *testing.T) {
	manifest := Manifest{}
	ok, err := manifest.CompatibleWith("v9.9.9")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want ok=true err=nil", ok, err)
	}
}

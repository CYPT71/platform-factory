package v1

import (
	"strings"
	"testing"
)

func TestIsRevokedByDigest(t *testing.T) {
	manifest := Manifest{Digest: "sha256:aaaa"}
	policy := TrustPolicy{RevokedDigests: []string{"sha256:aaaa"}}
	if !policy.IsRevoked(manifest) {
		t.Fatal("expected a manifest whose digest is on the revocation list to be revoked")
	}
}

func TestIsRevokedByKeyID(t *testing.T) {
	manifest := Manifest{Digest: "sha256:bbbb", Signature: &ManifestSignature{KeyID: "compromised-key"}}
	policy := TrustPolicy{RevokedKeyIDs: []string{"compromised-key"}}
	if !policy.IsRevoked(manifest) {
		t.Fatal("expected a manifest signed by a revoked key to be revoked, even though the digest itself isn't listed")
	}
}

func TestIsRevokedFalseWhenNeitherDigestNorKeyIsListed(t *testing.T) {
	manifest := Manifest{Digest: "sha256:cccc", Signature: &ManifestSignature{KeyID: "trusted-key"}}
	policy := TrustPolicy{RevokedKeyIDs: []string{"some-other-key"}, RevokedDigests: []string{"sha256:dddd"}}
	if policy.IsRevoked(manifest) {
		t.Fatal("a manifest matching neither revocation list must not be reported as revoked")
	}
}

func TestIsRevokedHandlesUnsignedManifestSafely(t *testing.T) {
	manifest := Manifest{Digest: "sha256:eeee"} // no Signature
	policy := TrustPolicy{RevokedKeyIDs: []string{"whatever"}}
	if policy.IsRevoked(manifest) {
		t.Fatal("an unsigned manifest has no key ID to match against RevokedKeyIDs")
	}
	// But an unsigned manifest's digest can still be revoked directly.
	policy = TrustPolicy{RevokedDigests: []string{"sha256:eeee"}}
	if !policy.IsRevoked(manifest) {
		t.Fatal("an unsigned manifest's digest must still be checked against RevokedDigests")
	}
}

func TestValidateRejectsLanguagePluginWithNetworkPermission(t *testing.T) {
	m := Manifest{
		APIVersion: ManifestAPIVersion, Name: "x", Version: "1.0.0",
		Family: PluginFamilyLanguage, Capabilities: []string{"language.build"},
		Executable: "x", Digest: "sha256:" + hexZeros64,
		Permissions: PluginPermissions{Network: []string{"example.com"}},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected an error for a language plugin declaring network permissions")
	}
}

func TestValidateAcceptsLanguagePluginWithNoNetworkPermission(t *testing.T) {
	m := Manifest{
		APIVersion: ManifestAPIVersion, Name: "x", Version: "1.0.0",
		Family: PluginFamilyLanguage, Capabilities: []string{"language.build"},
		Executable: "x", Digest: "sha256:" + hexZeros64,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAllowsNetworkPermissionForNonLanguageFamilies(t *testing.T) {
	m := Manifest{
		APIVersion: ManifestAPIVersion, Name: "x", Version: "1.0.0",
		Family: PluginFamilyDeployment, Capabilities: []string{"deployment.apply"},
		Executable: "x", Digest: "sha256:" + hexZeros64,
		Permissions: PluginPermissions{Network: []string{"kubernetes-api"}},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("a deployment plugin declaring network access should be allowed: %v", err)
	}
}

var hexZeros64 = strings.Repeat("0", 64)

func TestIsRevokedFalseByDefault(t *testing.T) {
	manifest := Manifest{Digest: "sha256:ffff", Signature: &ManifestSignature{KeyID: "any-key"}}
	var policy TrustPolicy // zero value: no revocation lists at all
	if policy.IsRevoked(manifest) {
		t.Fatal("a policy with no revocation lists must never report a manifest as revoked")
	}
}

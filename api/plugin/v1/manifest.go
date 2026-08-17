// Package v1 defines the stable plugin manifest and RPC contract.
package v1

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// ManifestAPIVersion identifies the plugin manifest schema.
	ManifestAPIVersion = "platform-factory.dev/plugin-manifest/v1"
	// LegacyManifestAPIVersion is the pre-rebrand identifier, still
	// accepted for the documented compatibility overlap window (see
	// docs/api-compatibility.md) - a plugin.json a deployment already
	// has on disk may not have been regenerated yet.
	LegacyManifestAPIVersion = "secure-oci.dev/plugin-manifest/v1"
	// ManifestFileName is the file a plugin directory must contain.
	ManifestFileName = "plugin.json"

	maxManifestBytes = 1 << 20
)

var (
	manifestNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	// manifestCapabilityPattern allows dot-notation for capabilities (family.action)
	manifestCapabilityPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)?$`)
)

// PluginFamily is which family of plugin a manifest declares itself as.
type PluginFamily string

const (
	// PluginFamilyLanguage is for plugins that handle source language detection and building.
	PluginFamilyLanguage PluginFamily = "language"
	// PluginFamilyAnalyzer is for plugins that analyze source code or artifacts.
	PluginFamilyAnalyzer PluginFamily = "analyzer"
	// PluginFamilyBuild is for plugins that perform build operations.
	PluginFamilyBuild PluginFamily = "build"
	// PluginFamilyRuntime is for plugins that manage runtime environments.
	PluginFamilyRuntime PluginFamily = "runtime"
	// PluginFamilyDeployment is for plugins that handle deployment to targets.
	PluginFamilyDeployment PluginFamily = "deployment"
	// PluginFamilyCapability is for plugins that provide cross-cutting capabilities.
	PluginFamilyCapability PluginFamily = "capability"
)

// PluginPermissions is the least-privilege declaration a manifest makes:
// every capability accessed outside the plugin's own confined workspace
// must be named here, not assumed.
type PluginPermissions struct {
	Network    []string `json:"network,omitempty"`
	Filesystem []string `json:"filesystem,omitempty"`
	Secrets    []string `json:"secrets,omitempty"`
}

// Manifest pins one plugin: its executable by digest, its identity and
// the capabilities it may advertise. A signature, when present, covers
// the canonical manifest bytes without the signature field.
type Manifest struct {
	APIVersion   string             `json:"api_version"`
	Name         string             `json:"name"`
	Version      string             `json:"version"`
	Capabilities []string           `json:"capabilities"`
	Family       PluginFamily       `json:"family,omitempty"`
	Platforms    []string           `json:"platforms,omitempty"`
	Permissions  PluginPermissions  `json:"permissions,omitempty"`
	Executable   string             `json:"executable"`
	Digest       string             `json:"digest"`
	Signature    *ManifestSignature `json:"signature,omitempty"`
}

// ManifestSignature is an Ed25519 signature over SigningBytes.
type ManifestSignature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id,omitempty"`
	Value     string `json:"value"`
}

// TrustPolicy decides which plugins the host will start. The executable
// digest in the manifest is always enforced; AllowUnsigned relaxes only
// the signature requirement, never the digest pin.
//
// Revocation takes precedence over trusted keys and AllowUnsigned.
type TrustPolicy struct {
	Keys          []ed25519.PublicKey
	AllowUnsigned bool
	// AllowUnsandboxedExecution explicitly permits starting a verified plugin
	// without process isolation when the host cannot enforce its sandbox.
	// It never relaxes digest, signature, revocation, or handshake checks.
	AllowUnsandboxedExecution bool
	RevokedKeyIDs             []string
	RevokedDigests            []string
}

// IsRevoked reports whether policy revokes manifest - either its
// executable digest or the key ID that signed it (if any) appears in
// the policy's revocation lists.
func (policy TrustPolicy) IsRevoked(manifest Manifest) bool {
	for _, digest := range policy.RevokedDigests {
		if digest == manifest.Digest {
			return true
		}
	}
	if manifest.Signature == nil {
		return false
	}
	for _, keyID := range policy.RevokedKeyIDs {
		if keyID == manifest.Signature.KeyID {
			return true
		}
	}
	return false
}

// LoadManifest reads and strictly validates dir/plugin.json.
func LoadManifest(dir string) (Manifest, error) {
	file, err := os.Open(filepath.Join(dir, ManifestFileName))
	if err != nil {
		return Manifest{}, fmt.Errorf("plugin manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxManifestBytes))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("plugin manifest: decode: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, errors.New("plugin manifest: must contain exactly one JSON object")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate checks every manifest field against the schema.
func (m Manifest) Validate() error {
	if m.APIVersion != ManifestAPIVersion && m.APIVersion != LegacyManifestAPIVersion {
		return fmt.Errorf("plugin manifest: unsupported api_version %q (want %q)", m.APIVersion, ManifestAPIVersion)
	}
	if !manifestNamePattern.MatchString(m.Name) {
		return fmt.Errorf("plugin manifest: invalid name %q", m.Name)
	}
	if m.Version == "" || strings.ContainsAny(m.Version, "\x00\n") {
		return errors.New("plugin manifest: version must be a non-empty single-line string")
	}
	if m.Family != "" {
		switch PluginFamily(m.Family) {
		case PluginFamilyLanguage, PluginFamilyAnalyzer, PluginFamilyBuild,
			PluginFamilyRuntime, PluginFamilyDeployment, PluginFamilyCapability:
			// valid
		default:
			return fmt.Errorf("plugin manifest: unknown family %q", m.Family)
		}
	}
	if len(m.Capabilities) == 0 {
		return errors.New("plugin manifest: at least one capability is required")
	}
	seen := map[string]bool{}
	for _, capability := range m.Capabilities {
		if !manifestCapabilityPattern.MatchString(capability) {
			return fmt.Errorf("plugin manifest: invalid capability %q", capability)
		}
		if seen[capability] {
			return fmt.Errorf("plugin manifest: duplicate capability %q", capability)
		}
		seen[capability] = true
	}
	for _, platform := range m.Platforms {
		if platform != "linux/amd64" && platform != "linux/arm64" {
			return fmt.Errorf("plugin manifest: unsupported platform %q", platform)
		}
	}
	// Language plugins cannot request host networking.
	if m.Family == PluginFamilyLanguage && len(m.Permissions.Network) > 0 {
		return fmt.Errorf("plugin manifest: language-family plugins may not declare network permissions, got %v", m.Permissions.Network)
	}
	if m.Executable == "" || path.Clean(m.Executable) != m.Executable ||
		path.IsAbs(m.Executable) || m.Executable == ".." ||
		strings.HasPrefix(m.Executable, "../") || strings.ContainsRune(m.Executable, 0) {
		return fmt.Errorf("plugin manifest: executable must be a clean relative path inside the plugin directory, got %q", m.Executable)
	}
	if !strings.HasPrefix(m.Digest, "sha256:") || len(m.Digest) != 71 {
		return fmt.Errorf("plugin manifest: digest must be sha256:<64 hex>, got %q", m.Digest)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(m.Digest, "sha256:")); err != nil {
		return fmt.Errorf("plugin manifest: invalid digest %q", m.Digest)
	}
	if m.Signature != nil {
		if m.Signature.Algorithm != "ed25519" {
			return fmt.Errorf("plugin manifest: unsupported signature algorithm %q", m.Signature.Algorithm)
		}
		if value, err := base64.StdEncoding.DecodeString(m.Signature.Value); err != nil || len(value) != ed25519.SignatureSize {
			return errors.New("plugin manifest: signature value must be a base64 Ed25519 signature")
		}
	}
	return nil
}

// SigningBytes returns the canonical bytes a manifest signature covers:
// the JSON encoding of the manifest with the signature field removed
// and capabilities/platforms sorted.
func (m Manifest) SigningBytes() ([]byte, error) {
	unsigned := m
	unsigned.Signature = nil
	unsigned.Capabilities = append([]string(nil), m.Capabilities...)
	sort.Strings(unsigned.Capabilities)
	unsigned.Platforms = append([]string(nil), m.Platforms...)
	sort.Strings(unsigned.Platforms)
	if len(unsigned.Platforms) == 0 {
		unsigned.Platforms = nil
	}
	return json.Marshal(unsigned)
}

// Sign signs the manifest with an Ed25519 private key and records the
// signature in place.
func (m *Manifest) Sign(key ed25519.PrivateKey, keyID string) error {
	payload, err := m.SigningBytes()
	if err != nil {
		return err
	}
	m.Signature = &ManifestSignature{
		Algorithm: "ed25519", KeyID: keyID,
		Value: base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload)),
	}
	return nil
}

// VerifySignature checks the manifest signature against every trusted
// key and succeeds when any of them verifies.
func (m Manifest) VerifySignature(keys []ed25519.PublicKey) error {
	if m.Signature == nil {
		return errors.New("plugin manifest: not signed")
	}
	payload, err := m.SigningBytes()
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(m.Signature.Value)
	if err != nil {
		return errors.New("plugin manifest: malformed signature")
	}
	for _, key := range keys {
		if ed25519.Verify(key, payload, signature) {
			return nil
		}
	}
	return errors.New("plugin manifest: signature does not verify against any trusted key")
}

// VerifyExecutable re-hashes the manifest's executable inside dir and
// compares it to the pinned digest.
func (m Manifest) VerifyExecutable(dir string) error {
	name := filepath.Join(dir, filepath.FromSlash(m.Executable))
	info, err := os.Lstat(name)
	if err != nil {
		return fmt.Errorf("plugin executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("plugin executable %s must be a regular file", m.Executable)
	}
	file, err := os.Open(name)
	if err != nil {
		return fmt.Errorf("plugin executable: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("plugin executable: %w", err)
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if digest != m.Digest {
		return fmt.Errorf("plugin executable digest %s does not match the manifest pin %s", digest, m.Digest)
	}
	return nil
}

// SameStringSet returns true if a and b contain the same strings (order-independent).
func SameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sortedA := append([]string(nil), a...)
	sortedB := append([]string(nil), b...)
	sort.Strings(sortedA)
	sort.Strings(sortedB)
	for index := range sortedA {
		if sortedA[index] != sortedB[index] {
			return false
		}
	}
	return true
}

// GetFamily returns the plugin family, defaulting to "unknown" if not set.
// GetFamily returns the declared family or "unknown".
func (m Manifest) GetFamily() PluginFamily {
	if m.Family == "" {
		return "unknown"
	}
	return m.Family
}

// HasCapability reports whether the manifest declares capability.
func (m Manifest) HasCapability(capability string) bool {
	for _, c := range m.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

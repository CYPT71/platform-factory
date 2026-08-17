// Package plugin owns the host-side plugin trust boundary. Public API and SDK packages are wire adapters only.
package plugin

import (
	"context"
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

	"github.com/CYPT71/platform-factory/internal/core"
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
	Keys []ed25519.PublicKey
	// TrustedKeys binds the manifest key_id to key material. When present,
	// relabeling a compromised key cannot bypass a revoked key identifier.
	TrustedKeys   map[string]ed25519.PublicKey
	AllowUnsigned bool
	// AllowUnsandboxedExecution explicitly permits starting a verified plugin
	// without process isolation when the host cannot enforce its sandbox.
	// It never relaxes digest, signature, revocation, or handshake checks.
	AllowUnsandboxedExecution bool
	RevokedKeyIDs             []string
	// RevokedKeyDigests contains sha256:<hex> fingerprints of raw Ed25519
	// public keys and remains effective regardless of manifest key_id labels.
	RevokedKeyDigests []string
	RevokedDigests    []string
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

func publicKeyDigest(key ed25519.PublicKey) string {
	digest := sha256.Sum256(key)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (policy TrustPolicy) verifySignature(manifest Manifest) error {
	if manifest.Signature == nil {
		return errors.New("plugin manifest: not signed")
	}
	var keys []ed25519.PublicKey
	if len(policy.TrustedKeys) != 0 {
		key, ok := policy.TrustedKeys[manifest.Signature.KeyID]
		if !ok {
			return fmt.Errorf("plugin manifest: signing key_id %q is not bound in the trust store", manifest.Signature.KeyID)
		}
		keys = []ed25519.PublicKey{key}
	} else {
		if len(policy.RevokedKeyIDs) != 0 {
			return errors.New("plugin manifest: revoked key IDs require a key_id-bound trust store")
		}
		keys = policy.Keys
	}
	if err := manifest.VerifySignature(keys); err != nil {
		return err
	}
	for _, key := range keys {
		if err := manifest.VerifySignature([]ed25519.PublicKey{key}); err != nil {
			continue
		}
		fingerprint := publicKeyDigest(key)
		for _, revoked := range policy.RevokedKeyDigests {
			if revoked == fingerprint {
				return fmt.Errorf("plugin manifest: signing key %s is revoked", fingerprint)
			}
		}
		break
	}
	return nil
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
	file, err := openPluginExecutable(dir, m.Executable)
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

var afterVerifiedExecutableSnapshot = func() {}
var afterPluginRootOpen = func() {}

// VerifyAndStart enforces the trust policy for the plugin in dir, then
// starts it and cross-checks the handshake against the manifest.
// Refusal is the default: an unsigned manifest starts only under
// policy.AllowUnsigned, and a digest mismatch never starts at all.
func VerifyAndStart(ctx context.Context, dir string, manifest Manifest, policy TrustPolicy) (*Client, error) {
	return verifyAndStart(ctx, dir, manifest, policy, nil)
}

// VerifyAndStartWithJournal injects the canonical idempotency port into a
// verified client. Mutating calls fail closed when no journal was injected.
func VerifyAndStartWithJournal(ctx context.Context, dir string, manifest Manifest, policy TrustPolicy, journal core.OperationJournal) (*Client, error) {
	if journal == nil {
		return nil, errors.New("plugin: operation journal is required")
	}
	return verifyAndStart(ctx, dir, manifest, policy, journal)
}

func verifyAndStart(ctx context.Context, dir string, manifest Manifest, policy TrustPolicy, journal core.OperationJournal) (*Client, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if policy.IsRevoked(manifest) {
		return nil, fmt.Errorf("plugin %s: refusing to start - revoked (digest %s or its signing key is on the revocation list)", manifest.Name, manifest.Digest)
	}
	if manifest.Signature == nil {
		if !policy.AllowUnsigned {
			return nil, fmt.Errorf("plugin %s: refusing unsigned manifest (pass --allow-unverified-plugin to accept unsigned plugins)", manifest.Name)
		}
	} else if err := policy.verifySignature(manifest); err != nil {
		return nil, fmt.Errorf("plugin %s: %w", manifest.Name, err)
	}
	snapshot, cleanup, err := verifiedExecutableSnapshot(dir, manifest)
	if err != nil {
		return nil, err
	}
	afterVerifiedExecutableSnapshot()
	startFn := StartWithManifest
	if policy.AllowUnsandboxedExecution {
		startFn = StartAllowingUnsandboxedWithManifest
	}
	client, err := startFn(ctx, snapshot, nil, nil, manifest.GetFamily(), manifest.Permissions)
	if err != nil {
		cleanup()
		return nil, err
	}
	client.setCleanup(cleanup)
	hello := client.Hello()
	if hello.Name != manifest.Name || hello.Version != manifest.Version ||
		!SameStringSet(hello.Capabilities, manifest.Capabilities) {
		_ = client.Close()
		return nil, fmt.Errorf("plugin %s: handshake identity (%s %s %v) does not match the manifest (%s %s %v)",
			manifest.Name, hello.Name, hello.Version, hello.Capabilities,
			manifest.Name, manifest.Version, manifest.Capabilities)
	}
	client.verifiedDigest = manifest.Digest
	client.journal = journal
	return client, nil
}

// verifiedExecutableSnapshot copies and hashes the same open file description,
// then executes the private immutable-by-path snapshot. Replacing the original
// pathname after verification therefore cannot substitute executable content.
func verifiedExecutableSnapshot(dir string, manifest Manifest) (string, func(), error) {
	source, err := openPluginExecutable(dir, manifest.Executable)
	if err != nil {
		return "", nil, fmt.Errorf("plugin executable: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("plugin executable: stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("plugin executable: %s is not a regular file", manifest.Executable)
	}
	snapshotDir, err := os.MkdirTemp("", "platform-factory-plugin-verified-*")
	if err != nil {
		return "", nil, fmt.Errorf("plugin executable: create verified snapshot: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(snapshotDir) }
	snapshot := filepath.Join(snapshotDir, "plugin")
	target, err := os.OpenFile(snapshot, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("plugin executable: create verified snapshot: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(target, hash), source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		cleanup()
		if copyErr != nil {
			return "", nil, fmt.Errorf("plugin executable: snapshot: %w", copyErr)
		}
		return "", nil, fmt.Errorf("plugin executable: close snapshot: %w", closeErr)
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if digest != manifest.Digest {
		cleanup()
		return "", nil, fmt.Errorf("plugin executable digest %s does not match the manifest pin %s", digest, manifest.Digest)
	}
	return snapshot, cleanup, nil
}

// VerifyStartAndPublishAvailable verifies and starts a previously discovered
// plugin, then atomically exposes its capabilities as available. Verification
// remains centralized in VerifyAndStart.
func VerifyStartAndPublishAvailable(ctx context.Context, registry *Registry, dir string, manifest Manifest, policy TrustPolicy) (*Client, error) {
	if registry == nil {
		return nil, fmt.Errorf("plugin %s: registry is required", manifest.Name)
	}
	client, err := VerifyAndStart(ctx, dir, manifest, policy)
	if err != nil {
		return nil, err
	}
	if err := registry.publishAvailable(manifest, client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("plugin %s: publish availability: %w", manifest.Name, err)
	}
	return client, nil
}

// VerifyStartAndPublishAvailableWithJournal is the mutating-capability entry
// point: availability is not published unless the durable journal was injected.
func VerifyStartAndPublishAvailableWithJournal(ctx context.Context, registry *Registry, dir string, manifest Manifest, policy TrustPolicy, journal core.OperationJournal) (*Client, error) {
	if registry == nil {
		return nil, fmt.Errorf("plugin %s: registry is required", manifest.Name)
	}
	client, err := VerifyAndStartWithJournal(ctx, dir, manifest, policy, journal)
	if err != nil {
		return nil, err
	}
	if err := registry.publishAvailable(manifest, client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("plugin %s: publish availability: %w", manifest.Name, err)
	}
	return client, nil
}

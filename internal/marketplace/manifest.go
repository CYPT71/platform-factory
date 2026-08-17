// Package marketplace implements a Go-modules-inspired plugin marketplace:
// plugins are never hosted directly, only discovered. Each plugin lives in
// its own Git repository, tags its releases with SemVer, and commits a
// plugin.yaml manifest at its root. This package indexes those
// repositories locally, searches the index, and installs a specific
// tagged version - the marketplace itself is a cache and a search/install
// convenience over Git, not a registry that owns plugin content.
package marketplace

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/mod/semver"
)

const (
	// ManifestAPIVersion identifies the plugin.yaml schema.
	ManifestAPIVersion = "platform-factory.dev/marketplace-manifest/v1"
	// ManifestFileName is the file every plugin repository must commit at
	// its root.
	ManifestFileName = "plugin.yaml"

	maxManifestBytes = 1 << 20
)

var (
	manifestNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	tagPattern          = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
)

// Permissions is the least-privilege declaration a manifest makes, in the
// same spirit as internal/plugin.PluginPermissions: every capability
// accessed outside the plugin's own workspace must be named here.
type Permissions struct {
	Network    []string `yaml:"network,omitempty"`
	Filesystem []string `yaml:"filesystem,omitempty"`
	Secrets    []string `yaml:"secrets,omitempty"`
}

// Signature is an Ed25519 signature over Manifest.SigningBytes, set by a
// publisher who wants their releases to show as verified in the index.
type Signature struct {
	Algorithm string `yaml:"algorithm"`
	KeyID     string `yaml:"key_id,omitempty"`
	Value     string `yaml:"value"`
}

// Manifest is a plugin's own declaration of itself: name, version,
// entrypoint, host compatibility, and requested permissions. It is
// committed as plugin.yaml at the repository root and read fresh at every
// tagged commit, so the manifest for v1.2.0 is exactly what was committed
// when v1.2.0 was tagged - there is no separate, driftable copy.
type Manifest struct {
	APIVersion string `yaml:"api_version"`
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`

	Description string   `yaml:"description,omitempty"`
	Author      string   `yaml:"author,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`

	// Entrypoint is a clean, relative, repository-internal path to what
	// platform-factory plugin load already knows how to run: a prebuilt
	// binary, or a Go/C#/Python/JavaScript/TypeScript/PHP source it can
	// build or interpret. This package does not reinterpret it - it is
	// handed unchanged to the same loader every other plugin source uses.
	Entrypoint string `yaml:"entrypoint"`

	// Compatibility is a list of SemVer constraints against the host
	// platform-factory version, ANDed together, e.g. [">=v1.0.0", "<v3.0.0"].
	// An empty list means "compatible with every host version" rather than
	// "compatible with none".
	Compatibility []string `yaml:"compatibility,omitempty"`

	Permissions Permissions `yaml:"permissions,omitempty"`
	Signature   *Signature  `yaml:"signature,omitempty"`
}

// DecodeManifest strictly decodes exactly one plugin.yaml document and
// validates it.
func DecodeManifest(r io.Reader) (Manifest, error) {
	decoder := yaml.NewDecoder(io.LimitReader(r, maxManifestBytes+1))
	decoder.KnownFields(true)
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("marketplace: decode plugin.yaml: %w", err)
	}
	if err := decoder.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("marketplace: plugin.yaml must contain exactly one document")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Encode renders the manifest as the plugin.yaml bytes a repository
// should commit - the inverse of DecodeManifest.
func (m Manifest) Encode() ([]byte, error) {
	return yaml.Marshal(m)
}

// Validate checks every manifest field against the schema. It never
// touches the filesystem or the network - callers decide what dir/repo
// the manifest came from.
func (m Manifest) Validate() error {
	if m.APIVersion != ManifestAPIVersion {
		return fmt.Errorf("marketplace: unsupported api_version %q (want %q)", m.APIVersion, ManifestAPIVersion)
	}
	if !manifestNamePattern.MatchString(m.Name) {
		return fmt.Errorf("marketplace: invalid name %q", m.Name)
	}
	if !semver.IsValid(normalizeVersion(m.Version)) {
		return fmt.Errorf("marketplace: version %q is not valid SemVer", m.Version)
	}
	if m.Entrypoint == "" {
		return errors.New("marketplace: entrypoint is required")
	}
	if cleaned := cleanRelativePath(m.Entrypoint); cleaned == "" {
		return fmt.Errorf("marketplace: entrypoint must be a clean relative path inside the repository, got %q", m.Entrypoint)
	}
	for _, constraint := range m.Compatibility {
		if _, _, err := parseConstraint(constraint); err != nil {
			return fmt.Errorf("marketplace: invalid compatibility constraint %q: %w", constraint, err)
		}
	}
	seen := map[string]bool{}
	for _, tag := range m.Tags {
		if tag == "" || strings.ContainsAny(tag, "\x00\n") {
			return fmt.Errorf("marketplace: invalid tag %q", tag)
		}
		if seen[tag] {
			return fmt.Errorf("marketplace: duplicate tag %q", tag)
		}
		seen[tag] = true
	}
	if m.Signature != nil {
		if m.Signature.Algorithm != "ed25519" {
			return fmt.Errorf("marketplace: unsupported signature algorithm %q", m.Signature.Algorithm)
		}
		if value, err := base64.StdEncoding.DecodeString(m.Signature.Value); err != nil || len(value) != ed25519.SignatureSize {
			return errors.New("marketplace: signature value must be a base64 Ed25519 signature")
		}
	}
	return nil
}

// cleanRelativePath returns candidate if it is already a clean, relative,
// repository-internal path, or "" if it escapes the repository, is
// absolute, or contains a NUL byte.
func cleanRelativePath(candidate string) string {
	if candidate == "" || strings.ContainsRune(candidate, 0) || path.IsAbs(candidate) {
		return ""
	}
	if path.Clean(candidate) != candidate || candidate == ".." || strings.HasPrefix(candidate, "../") {
		return ""
	}
	return candidate
}

// SigningBytes returns the canonical bytes a manifest signature covers:
// the YAML-independent, deterministic encoding of every field except the
// signature itself.
func (m Manifest) SigningBytes() []byte {
	compatibility := append([]string(nil), m.Compatibility...)
	sort.Strings(compatibility)
	tags := append([]string(nil), m.Tags...)
	sort.Strings(tags)
	network := append([]string(nil), m.Permissions.Network...)
	filesystem := append([]string(nil), m.Permissions.Filesystem...)
	secrets := append([]string(nil), m.Permissions.Secrets...)
	sort.Strings(network)
	sort.Strings(filesystem)
	sort.Strings(secrets)
	var b strings.Builder
	fmt.Fprintf(&b, "api_version=%s\n", m.APIVersion)
	fmt.Fprintf(&b, "name=%s\n", m.Name)
	fmt.Fprintf(&b, "version=%s\n", m.Version)
	fmt.Fprintf(&b, "description=%s\n", m.Description)
	fmt.Fprintf(&b, "author=%s\n", m.Author)
	fmt.Fprintf(&b, "entrypoint=%s\n", m.Entrypoint)
	fmt.Fprintf(&b, "compatibility=%s\n", strings.Join(compatibility, ","))
	fmt.Fprintf(&b, "tags=%s\n", strings.Join(tags, ","))
	fmt.Fprintf(&b, "permissions.network=%s\n", strings.Join(network, ","))
	fmt.Fprintf(&b, "permissions.filesystem=%s\n", strings.Join(filesystem, ","))
	fmt.Fprintf(&b, "permissions.secrets=%s\n", strings.Join(secrets, ","))
	return []byte(b.String())
}

// Sign signs the manifest in place with an Ed25519 private key.
func (m *Manifest) Sign(key ed25519.PrivateKey, keyID string) {
	m.Signature = &Signature{
		Algorithm: "ed25519",
		KeyID:     keyID,
		Value:     base64.StdEncoding.EncodeToString(ed25519.Sign(key, m.SigningBytes())),
	}
}

// VerifySignature checks the manifest's signature against every trusted
// key and succeeds if any one of them verifies.
func (m Manifest) VerifySignature(keys []ed25519.PublicKey) error {
	if m.Signature == nil {
		return errors.New("marketplace: manifest is not signed")
	}
	signature, err := base64.StdEncoding.DecodeString(m.Signature.Value)
	if err != nil {
		return errors.New("marketplace: malformed signature")
	}
	payload := m.SigningBytes()
	for _, key := range keys {
		if ed25519.Verify(key, payload, signature) {
			return nil
		}
	}
	return errors.New("marketplace: signature does not verify against any trusted key")
}

// CompatibleWith reports whether hostVersion (SemVer, "v" prefix
// optional) satisfies every constraint the manifest declares. A manifest
// with no constraints is compatible with every host version.
func (m Manifest) CompatibleWith(hostVersion string) (bool, error) {
	host := normalizeVersion(hostVersion)
	if !semver.IsValid(host) {
		return false, fmt.Errorf("marketplace: invalid host version %q", hostVersion)
	}
	for _, constraint := range m.Compatibility {
		op, version, err := parseConstraint(constraint)
		if err != nil {
			return false, err
		}
		cmp := semver.Compare(host, version)
		var ok bool
		switch op {
		case "", "=":
			ok = cmp == 0
		case ">=":
			ok = cmp >= 0
		case ">":
			ok = cmp > 0
		case "<=":
			ok = cmp <= 0
		case "<":
			ok = cmp < 0
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

var constraintOperators = []string{">=", "<=", ">", "<", "="}

func parseConstraint(constraint string) (op, version string, err error) {
	constraint = strings.TrimSpace(constraint)
	for _, candidate := range constraintOperators {
		if strings.HasPrefix(constraint, candidate) {
			op = candidate
			version = strings.TrimSpace(strings.TrimPrefix(constraint, candidate))
			break
		}
	}
	if op == "" {
		version = constraint
	}
	version = normalizeVersion(version)
	if !semver.IsValid(version) {
		return "", "", fmt.Errorf("not a valid SemVer constraint")
	}
	return op, version, nil
}

// normalizeVersion adds the "v" prefix golang.org/x/mod/semver requires,
// if the caller omitted it.
func normalizeVersion(version string) string {
	version = strings.TrimSpace(version)
	if version != "" && version[0] != 'v' {
		version = "v" + version
	}
	return version
}

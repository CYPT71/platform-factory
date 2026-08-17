package provenance

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/signing"
)

// PluginBuildInputs is everything mvp.md's "Add verifiable build
// provenance of plugin" checklist item names as required content - source
// commit, builder identity, build inputs - kept separate from the I/O that
// captures them (CapturePluginSourceCommit, DigestPluginExecutable) so
// GeneratePluginProvenance itself stays a pure, deterministic function of
// its inputs, not a function of the filesystem or git state at call time.
type PluginBuildInputs struct {
	// PluginName is the plugin's manifest name (Manifest.Name).
	PluginName string
	// PluginVersion is the plugin's manifest version (Manifest.Version).
	PluginVersion string
	// ArtifactDigest is the built executable's sha256 digest, in the same
	// "sha256:<hex>" form as Manifest.Digest - this predicate is only
	// meaningful when it names the exact bytes a manifest pins.
	ArtifactDigest string
	// SourceCommit is the git commit the executable was built from.
	SourceCommit string
	// SourceDirty reports whether the working tree had uncommitted changes
	// at build time - a provenance record for a dirty tree is honest about
	// it rather than silently citing a commit that does not reproduce the
	// actual bytes.
	SourceDirty bool
	// ModulePath is the plugin's own Go module path (its go.mod module
	// directive), identifying which of this repo's several modules
	// (plugins/kubevirt, plugins/containerd, ...) produced the artifact.
	ModulePath string
	// GoVersion is the toolchain that built it (runtime.Version()).
	GoVersion string
	// BuilderID identifies who/what ran the build - an operator-supplied
	// value, the same role --builder-id already plays for publish's own
	// SLSA provenance (lifecycle.go).
	BuilderID string
}

func (i PluginBuildInputs) validate() error {
	switch {
	case i.PluginName == "":
		return errors.New("plugin provenance: plugin name is required")
	case i.ArtifactDigest == "":
		return errors.New("plugin provenance: artifact digest is required")
	case !strings.HasPrefix(i.ArtifactDigest, "sha256:") || len(i.ArtifactDigest) != 71:
		return fmt.Errorf("plugin provenance: artifact digest must be sha256:<64 hex>, got %q", i.ArtifactDigest)
	case i.SourceCommit == "":
		return errors.New("plugin provenance: source commit is required")
	case i.BuilderID == "":
		return errors.New("plugin provenance: builder id is required")
	}
	return nil
}

// GeneratePluginProvenance builds the signed-predicate content - source,
// builder identity, artifact and digest, associated together - that
// mvp.md's provenance items ask for, reusing this package's own
// ProvenanceRecord/Material/Invocation/ConfigSource shape (signing.go)
// instead of inventing a second predicate format: a plugin's build
// provenance and an OCI image's build provenance (Generate, provenance.go)
// are the same kind of claim about two different kinds of artifact.
func GeneratePluginProvenance(inputs PluginBuildInputs) (ProvenanceRecord, error) {
	if err := inputs.validate(); err != nil {
		return ProvenanceRecord{}, err
	}
	buildID := inputs.PluginName + "@" + inputs.ArtifactDigest
	return ProvenanceRecord{
		BuildID:    buildID,
		ArtifactID: inputs.ArtifactDigest,
		WorkerID:   inputs.BuilderID,
		Materials: []Material{{
			URI:      "git+source:" + inputs.ModulePath,
			Digest:   inputs.SourceCommit,
			MIMEType: "text/x-git-commit",
		}},
		Invocation: Invocation{
			ConfigSource: ConfigSource{
				URI: inputs.ModulePath, Digest: inputs.SourceCommit, EntryPoint: "go build",
			},
			Environment: map[string]string{
				"go_version":     inputs.GoVersion,
				"source_dirty":   strconv.FormatBool(inputs.SourceDirty),
				"plugin_name":    inputs.PluginName,
				"plugin_version": inputs.PluginVersion,
			},
		},
		Timestamp: time.Now().UTC(),
	}, nil
}

// CapturePluginSourceCommit runs git in dir to capture the commit a
// plugin's source tree is at, and whether the working tree has
// uncommitted changes relative to it - real capture, not an assumption:
// a caller with no git history at all (a source tarball, for example)
// gets an explicit error instead of a fabricated commit.
func CapturePluginSourceCommit(dir string) (commit string, dirty bool, err error) {
	revParse := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	var out bytes.Buffer
	revParse.Stdout = &out
	if err := revParse.Run(); err != nil {
		return "", false, fmt.Errorf("plugin provenance: capture source commit: %w", err)
	}
	commit = strings.TrimSpace(out.String())
	if commit == "" {
		return "", false, errors.New("plugin provenance: git rev-parse HEAD returned no commit")
	}

	status := exec.Command("git", "-C", dir, "status", "--porcelain")
	var statusOut bytes.Buffer
	status.Stdout = &statusOut
	if err := status.Run(); err != nil {
		return "", false, fmt.Errorf("plugin provenance: capture working tree status: %w", err)
	}
	dirty = strings.TrimSpace(statusOut.String()) != ""
	return commit, dirty, nil
}

// DigestPluginExecutable hashes the built plugin binary at path, in the
// same "sha256:<64 hex>" form Manifest.Digest requires - the provenance
// record's ArtifactID is only meaningful when it is computed the same way
// the manifest's own pin was.
func DigestPluginExecutable(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("plugin provenance: open executable: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("plugin provenance: hash executable: %w", err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// signingBytes is the exact serialization WorkloadSigner.Sign/Verify
// already use (record with Signature/SignedBy cleared) - reused here so a
// plugin provenance record signed by SignPluginProvenance verifies with
// the same Verify function any other ProvenanceRecord in this package
// does, not a parallel signature format.
func signingBytes(record ProvenanceRecord) ([]byte, error) {
	record.Signature = ""
	record.SignedBy = ""
	return json.Marshal(record)
}

// SignPluginProvenance signs record with store's key keyName, the same
// signing.KeyStore interface and --key-dir/--key-name convention
// publish's own --sign flag already uses (lifecycle.go), rather than
// requiring a raw ed25519.PrivateKey the way WorkloadSigner.Sign does -
// this keeps plugin provenance signing on the same key-management path an
// operator already uses for everything else this CLI signs.
func SignPluginProvenance(record ProvenanceRecord, store signing.KeyStore, keyName string) (ProvenanceRecord, error) {
	data, err := signingBytes(record)
	if err != nil {
		return ProvenanceRecord{}, fmt.Errorf("plugin provenance: encode record: %w", err)
	}
	signature, err := store.Sign(keyName, data)
	if err != nil {
		return ProvenanceRecord{}, fmt.Errorf("plugin provenance: sign: %w", err)
	}
	record.Signature = base64.StdEncoding.EncodeToString(signature)
	record.SignedBy = keyName
	return record, nil
}

// VerifyPluginProvenance verifies record's signature against store's
// public key keyName - the read side of SignPluginProvenance, using the
// same signingBytes serialization.
func VerifyPluginProvenance(record ProvenanceRecord, store signing.KeyStore, keyName string) error {
	if record.Signature == "" {
		return errors.New("plugin provenance: record is not signed")
	}
	publicKey, err := store.PublicKey(keyName)
	if err != nil {
		return fmt.Errorf("plugin provenance: load public key: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(record.Signature)
	if err != nil {
		return fmt.Errorf("plugin provenance: decode signature: %w", err)
	}
	data, err := signingBytes(record)
	if err != nil {
		return fmt.Errorf("plugin provenance: encode record: %w", err)
	}
	if err := signing.Verify(publicKey, data, signature); err != nil {
		return fmt.Errorf("plugin provenance: %w", err)
	}
	return nil
}

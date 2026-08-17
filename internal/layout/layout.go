// Package layout inspects and strictly verifies OCI image layouts.
package layout

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type Report struct {
	Path      string     `json:"path"`
	Valid     bool       `json:"valid"`
	Platforms []Platform `json:"platforms"`
	Manifests int        `json:"manifests"`
	Blobs     int        `json:"blobs"`
}

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Digest       string `json:"digest"`
	Reference    string `json:"reference,omitempty"`
}

type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *Platform         `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type index struct {
	SchemaVersion int          `json:"schemaVersion"`
	Manifests     []descriptor `json:"manifests"`
}

type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

type imageConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	RootFS       struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

// 64 MiB comfortably fit CPython (the only bundled interpreter with a
// provision-runtime path), but a stock Node.js interpreter binary alone is
// ~100-130 MiB (V8 + full ICU), so a "runtime:"-based Node project could
// never pass this check. Raised to fit a real single-binary interpreter
// plus its shared-library closure, while still bounding layer size.
const maxLayerBytes int64 = 256 << 20
const maxTotalLayerBytes int64 = 512 << 20
const maxLayerEntries = 100000

// Verify is the strict, default check used everywhere a layout might be
// published, signed, or otherwise trusted beyond the machine that built
// it: pf verify, pf publish's pre-push gate, pf build/pf project build's
// self-check. It scans every layer for embedded-secret markers.
func Verify(rootName string) (Report, error) {
	return verify(rootName, verifyOptions{scanForSecrets: true})
}

// VerifyForLocalImport performs the exact same structural and digest
// verification as Verify, but skips the embedded-secret-marker scan.
// It exists for exactly one caller: pf import's local-load path, where
// the layout was just produced by this same trusted local invocation of
// cmd/oci-builder from this repository's own source and never leaves
// the machine - the threat Verify's secret scan defends against (a
// bundled directory tree's leaked credential ending up pushed to a
// shared registry) does not apply to loading a layout into the local
// container runtime. A side effect this repository has hit in practice:
// the compiled platform-factory binary statically embeds the scanner's
// own marker strings (internal/layout/archive.go's assignmentSecretMarkers
// etc. are Go string literals, compiled into the binary's rodata), so
// Verify always self-flags a layer containing platform-factory's own
// binary - a false positive with no actual secret involved. Do not call
// this for anything that publishes, signs, or otherwise extends trust
// past the local machine; use Verify there.
func VerifyForLocalImport(rootName string) (Report, error) {
	return verify(rootName, verifyOptions{scanForSecrets: false})
}

type verifyOptions struct {
	scanForSecrets bool
}

func verify(rootName string, opts verifyOptions) (Report, error) {
	root := filepath.Clean(rootName)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Report{}, errors.New("layout root must be a real directory")
	}
	marker, err := regular(filepath.Join(root, "oci-layout"))
	if err != nil || string(marker) != "{\"imageLayoutVersion\":\"1.0.0\"}\n" {
		return Report{}, errors.New("invalid OCI layout marker")
	}
	var idx index
	if err := decodeFile(filepath.Join(root, "index.json"), &idx); err != nil {
		return Report{}, fmt.Errorf("index: %w", err)
	}
	if idx.SchemaVersion != 2 || len(idx.Manifests) == 0 {
		return Report{}, errors.New("index must contain at least one schema-2 manifest")
	}
	report := Report{Path: rootName, Valid: true, Manifests: len(idx.Manifests)}
	layerBudget := maxTotalLayerBytes
	expected := map[string]bool{}
	seenReferences := map[string]bool{}
	for _, manifestDescriptor := range idx.Manifests {
		if manifestDescriptor.Platform == nil {
			return Report{}, errors.New("manifest descriptor has no platform")
		}
		platformKey := manifestDescriptor.Platform.OS + "/" + manifestDescriptor.Platform.Architecture
		reference := manifestDescriptor.Annotations["org.opencontainers.image.ref.name"]
		referenceKey := reference + "\x00" + platformKey
		if manifestDescriptor.Platform.OS != "linux" ||
			(manifestDescriptor.Platform.Architecture != "amd64" && manifestDescriptor.Platform.Architecture != "arm64") ||
			seenReferences[referenceKey] {
			return Report{}, fmt.Errorf("unsupported or duplicate reference/platform %q %s", reference, platformKey)
		}
		seenReferences[referenceKey] = true
		manifestData, err := readDescriptor(root, manifestDescriptor, expected)
		if err != nil {
			return Report{}, err
		}
		var document manifest
		if err := json.Unmarshal(manifestData, &document); err != nil || document.SchemaVersion != 2 || len(document.Layers) == 0 {
			return Report{}, errors.New("invalid manifest")
		}
		configData, err := readDescriptor(root, document.Config, expected)
		if err != nil {
			return Report{}, err
		}
		var config imageConfig
		if err := json.Unmarshal(configData, &config); err != nil {
			return Report{}, errors.New("invalid image config")
		}
		if config.OS != manifestDescriptor.Platform.OS || config.Architecture != manifestDescriptor.Platform.Architecture ||
			config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(document.Layers) {
			return Report{}, errors.New("config platform or rootfs does not match manifest")
		}
		for index, layerDescriptor := range document.Layers {
			layerData, err := readDescriptor(root, layerDescriptor, expected)
			if err != nil {
				return Report{}, err
			}
			if err := verifyLayerWithBudget(layerData, config.RootFS.DiffIDs[index], &layerBudget, opts); err != nil {
				return Report{}, err
			}
		}
		report.Platforms = append(report.Platforms, *manifestDescriptor.Platform)
		report.Platforms[len(report.Platforms)-1].Digest = manifestDescriptor.Digest
		report.Platforms[len(report.Platforms)-1].Reference = reference
	}
	entries, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
	if err != nil {
		return Report{}, err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			report.Blobs++
		}
		if !entry.Type().IsRegular() || !expected[entry.Name()] {
			return Report{}, fmt.Errorf("unexpected blob entry %q", entry.Name())
		}
	}
	if report.Blobs != len(expected) {
		return Report{}, errors.New("blob set is incomplete")
	}
	return report, nil
}

func Inspect(root string) (Report, error) { return Verify(root) }

func decodeFile(filename string, target any) error {
	data, err := regular(filename)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	return nil
}

func regular(filename string) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filename)
	}
	return os.ReadFile(filename)
}

func readDescriptor(root string, value descriptor, expected map[string]bool) ([]byte, error) {
	if !strings.HasPrefix(value.Digest, "sha256:") || len(value.Digest) != 71 {
		return nil, fmt.Errorf("invalid descriptor digest %q", value.Digest)
	}
	hexDigest := value.Digest[7:]
	if _, err := hex.DecodeString(hexDigest); err != nil {
		return nil, fmt.Errorf("invalid descriptor digest %q", value.Digest)
	}
	data, err := regular(filepath.Join(root, "blobs", "sha256", hexDigest))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hexDigest || int64(len(data)) != value.Size {
		return nil, fmt.Errorf("digest or size mismatch for %s", value.Digest)
	}
	expected[hexDigest] = true
	return data, nil
}

func verifyLayer(data []byte, diffID string) error {
	budget := maxTotalLayerBytes
	return verifyLayerWithBudget(data, diffID, &budget, verifyOptions{scanForSecrets: true})
}
func verifyLayerWithBudget(data []byte, diffID string, budget *int64, opts verifyOptions) error {
	compressed := bufio.NewReader(bytes.NewReader(data))
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		return errors.New("invalid gzip layer")
	}
	reader.Multistream(false)
	raw, err := io.ReadAll(io.LimitReader(reader, maxLayerBytes+1))
	closeErr := reader.Close()
	if err != nil || closeErr != nil || int64(len(raw)) > maxLayerBytes {
		return errors.New("invalid compressed layer")
	}
	if _, err := compressed.Peek(1); !errors.Is(err, io.EOF) {
		return errors.New("compressed layer has trailing or concatenated data")
	}
	if budget == nil || int64(len(raw)) > *budget {
		return errors.New("layer global size limit exceeded")
	}
	*budget -= int64(len(raw))
	if opts.scanForSecrets && containsSecretMarker(raw) {
		return errors.New("layer contains forbidden secret marker")
	}
	sum := sha256.Sum256(raw)
	if diffID != "sha256:"+hex.EncodeToString(sum[:]) {
		return errors.New("layer diff_id mismatch")
	}
	archive := tar.NewReader(bytes.NewReader(raw))
	seen := map[string]bool{}
	entries := 0
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("invalid tar layer")
		}
		entries++
		if entries > maxLayerEntries {
			return errors.New("layer entry limit exceeded")
		}
		if header.Size < 0 || header.Size > maxLayerBytes {
			return errors.New("layer entry size limit exceeded")
		}
		clean := strings.TrimSuffix(header.Name, "/")
		if clean == "" || strings.HasPrefix(clean, "/") || path.Clean(clean) != clean || seen[clean] {
			return fmt.Errorf("unsafe or duplicate layer path %q", header.Name)
		}
		seen[clean] = true
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
			return fmt.Errorf("unsafe layer entry type for %q", header.Name)
		}
	}
	return nil
}

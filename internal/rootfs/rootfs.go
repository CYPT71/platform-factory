// Package rootfs converts a verified local OCI image layout into a safe,
// deterministic filesystem tree without invoking external tools.
package rootfs

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxJSONBytes = 8 << 20

const (
	defaultMaxBytes     = int64(64 << 30)
	defaultMaxFiles     = 1_000_000
	defaultMaxFileBytes = int64(8 << 30)
)

type Options struct {
	Layout    string
	Output    string
	Platform  string
	Reference string
	// MaxBytes limits total uncompressed layer bytes, including tar
	// metadata and padding. MaxFiles limits archive entries (not merely
	// regular files), preventing directory/whiteout header bombs.
	MaxBytes     int64
	MaxFiles     int
	MaxFileBytes int64
}

type Result struct {
	ManifestDigest string `json:"manifest_digest"`
	RootFSDigest   string `json:"rootfs_digest"`
	Files          int    `json:"files"`
	Bytes          int64  `json:"bytes"`
}

type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *platform         `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}
type platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
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
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	RootFS       struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

// Convert verifies the selected manifest, config, compressed layer digests and
// uncompressed diff IDs while streaming layers into a temporary directory.
// Output is installed atomically only after every check succeeds.
func Convert(opts Options) (result Result, err error) {
	if opts.Layout == "" || opts.Output == "" {
		return Result{}, errors.New("rootfs: layout and output are required")
	}
	budget, err := newExtractionBudget(opts)
	if err != nil {
		return Result{}, err
	}
	layout, err := realDirectory(opts.Layout)
	if err != nil {
		return Result{}, fmt.Errorf("rootfs: layout: %w", err)
	}
	markerName := filepath.Join(layout, "oci-layout")
	markerInfo, markerErr := os.Lstat(markerName)
	marker, err := os.ReadFile(markerName)
	if markerErr != nil || !markerInfo.Mode().IsRegular() || err != nil ||
		string(marker) != "{\"imageLayoutVersion\":\"1.0.0\"}\n" {
		return Result{}, errors.New("rootfs: invalid OCI layout marker")
	}
	if _, err := os.Lstat(opts.Output); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Result{}, errors.New("rootfs: output already exists")
		}
		return Result{}, fmt.Errorf("rootfs: inspect output: %w", err)
	}
	var idx index
	if err := readJSONFile(filepath.Join(layout, "index.json"), &idx); err != nil {
		return Result{}, fmt.Errorf("rootfs: index: %w", err)
	}
	if idx.SchemaVersion != 2 {
		return Result{}, errors.New("rootfs: index schemaVersion must be 2")
	}
	selected, err := selectManifest(idx.Manifests, opts.Platform, opts.Reference)
	if err != nil {
		return Result{}, err
	}
	manifestBytes, err := readVerifiedBlob(layout, selected)
	if err != nil {
		return Result{}, fmt.Errorf("rootfs: manifest: %w", err)
	}
	var document manifest
	if err := json.Unmarshal(manifestBytes, &document); err != nil || document.SchemaVersion != 2 || len(document.Layers) == 0 {
		return Result{}, errors.New("rootfs: invalid manifest")
	}
	configBytes, err := readVerifiedBlob(layout, document.Config)
	if err != nil {
		return Result{}, fmt.Errorf("rootfs: config: %w", err)
	}
	var config imageConfig
	if err := json.Unmarshal(configBytes, &config); err != nil ||
		config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(document.Layers) {
		return Result{}, errors.New("rootfs: invalid image config")
	}
	if selected.Platform != nil &&
		(config.OS != selected.Platform.OS || config.Architecture != selected.Platform.Architecture) {
		return Result{}, errors.New("rootfs: config platform does not match manifest descriptor")
	}

	parent := filepath.Dir(filepath.Clean(opts.Output))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Result{}, fmt.Errorf("rootfs: create output parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".platform-factory-rootfs-*")
	if err != nil {
		return Result{}, fmt.Errorf("rootfs: create temporary rootfs: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(temporary)
		}
	}()
	confined, err := os.OpenRoot(temporary)
	if err != nil {
		return Result{}, fmt.Errorf("rootfs: confine temporary rootfs: %w", err)
	}
	defer confined.Close()
	for layerIndex, layer := range document.Layers {
		if err := applyLayer(layout, confined, layer, config.RootFS.DiffIDs[layerIndex], budget); err != nil {
			return Result{}, fmt.Errorf("rootfs: layer %d: %w", layerIndex, err)
		}
	}
	if err := confined.Close(); err != nil {
		return Result{}, fmt.Errorf("rootfs: close confined rootfs: %w", err)
	}
	digest, files, bytes, err := digestTree(temporary)
	if err != nil {
		return Result{}, err
	}
	if err := os.Rename(temporary, opts.Output); err != nil {
		return Result{}, fmt.Errorf("rootfs: install output: %w", err)
	}
	return Result{ManifestDigest: selected.Digest, RootFSDigest: digest, Files: files, Bytes: bytes}, nil
}

func selectManifest(manifests []descriptor, wantedPlatform, wantedReference string) (descriptor, error) {
	var matches []descriptor
	for _, candidate := range manifests {
		candidatePlatform := ""
		if candidate.Platform != nil {
			candidatePlatform = candidate.Platform.OS + "/" + candidate.Platform.Architecture
		}
		if wantedPlatform != "" && candidatePlatform != wantedPlatform {
			continue
		}
		if wantedReference != "" && candidate.Annotations["org.opencontainers.image.ref.name"] != wantedReference {
			continue
		}
		matches = append(matches, candidate)
	}
	if len(matches) != 1 {
		return descriptor{}, fmt.Errorf("rootfs: manifest selection matched %d entries; specify an unambiguous platform/reference", len(matches))
	}
	if matches[0].Platform == nil || matches[0].Platform.OS != "linux" {
		return descriptor{}, errors.New("rootfs: selected manifest must describe a Linux platform")
	}
	return matches[0], nil
}

type extractionBudget struct {
	maxBytes     int64
	maxFiles     int
	maxFileBytes int64
	bytes        int64
	files        int
}

func newExtractionBudget(opts Options) (*extractionBudget, error) {
	if opts.MaxBytes < 0 || opts.MaxFiles < 0 || opts.MaxFileBytes < 0 {
		return nil, errors.New("rootfs: extraction budgets must not be negative")
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	if opts.MaxFiles == 0 {
		opts.MaxFiles = defaultMaxFiles
	}
	if opts.MaxFileBytes == 0 {
		opts.MaxFileBytes = defaultMaxFileBytes
	}
	if opts.MaxFileBytes > opts.MaxBytes {
		return nil, errors.New("rootfs: MaxFileBytes must not exceed MaxBytes")
	}
	return &extractionBudget{
		maxBytes: opts.MaxBytes, maxFiles: opts.MaxFiles, maxFileBytes: opts.MaxFileBytes,
	}, nil
}

type budgetReader struct {
	source io.Reader
	budget *extractionBudget
}

func (r *budgetReader) Read(buffer []byte) (int, error) {
	remaining := r.budget.maxBytes - r.budget.bytes
	if remaining <= 0 {
		var probe [1]byte
		n, err := r.source.Read(probe[:])
		if n > 0 {
			return 0, errors.New("uncompressed layers exceed MaxBytes")
		}
		return 0, err
	}
	if int64(len(buffer)) > remaining+1 {
		buffer = buffer[:remaining+1]
	}
	n, err := r.source.Read(buffer)
	if int64(n) > remaining {
		r.budget.bytes += int64(n)
		return 0, errors.New("uncompressed layers exceed MaxBytes")
	}
	r.budget.bytes += int64(n)
	return n, err
}

func applyLayer(layout string, root *os.Root, desc descriptor, diffID string, budget *extractionBudget) error {
	file, err := openBlob(layout, desc)
	if err != nil {
		return err
	}
	defer file.Close()
	compressedHash := sha256.New()
	compressed := io.TeeReader(io.LimitReader(file, desc.Size+1), compressedHash)
	var uncompressed io.ReadCloser
	switch desc.MediaType {
	case "application/vnd.oci.image.layer.v1.tar+gzip",
		"application/vnd.docker.image.rootfs.diff.tar.gzip":
		uncompressed, err = gzip.NewReader(compressed)
	case "application/vnd.oci.image.layer.v1.tar",
		"application/vnd.docker.image.rootfs.diff.tar":
		uncompressed = io.NopCloser(compressed)
	default:
		return fmt.Errorf("unsupported layer media type %q", desc.MediaType)
	}
	if err != nil {
		return fmt.Errorf("open compressed layer: %w", err)
	}
	rawHash := sha256.New()
	raw := &budgetReader{source: uncompressed, budget: budget}
	archive := tar.NewReader(io.TeeReader(raw, rawHash))
	seen := map[string]bool{}
	for {
		header, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read tar: %w", nextErr)
		}
		budget.files++
		if budget.files > budget.maxFiles {
			return errors.New("archive entries exceed MaxFiles")
		}
		if header.Size < 0 || header.Size > budget.maxFileBytes {
			return fmt.Errorf("layer entry %q exceeds MaxFileBytes", header.Name)
		}
		name, err := safeArchivePath(header.Name)
		if err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("duplicate layer path %q", name)
		}
		seen[name] = true
		if handled, err := applyWhiteout(root, name); handled || err != nil {
			if err != nil {
				return err
			}
			continue
		}
		target := filepath.FromSlash(name)
		switch header.Typeflag {
		case tar.TypeDir:
			if info, err := root.Lstat(target); err == nil && !info.IsDir() {
				if err := root.Remove(target); err != nil {
					return err
				}
			}
			if err := root.MkdirAll(target, 0o755); err != nil {
				return err
			}
			if err := root.Chmod(target, normalizedMode(header.Mode, true)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := root.RemoveAll(target); err != nil {
				return err
			}
			output, err := root.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, normalizedMode(header.Mode, false))
			if err != nil {
				return err
			}
			written, copyErr := io.CopyN(output, archive, header.Size)
			closeErr := output.Close()
			if copyErr != nil || closeErr != nil || written != header.Size {
				return errors.New("truncated layer file")
			}
			if err := root.Chmod(target, normalizedMode(header.Mode, false)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := validateSymlinkTarget(name, header.Linkname); err != nil {
				return err
			}
			if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := root.RemoveAll(target); err != nil {
				return err
			}
			if err := root.Symlink(filepath.FromSlash(header.Linkname), target); err != nil {
				return fmt.Errorf("create symlink %q: %w", name, err)
			}
		case tar.TypeLink:
			linkTarget, err := safeArchivePath(header.Linkname)
			if err != nil {
				return fmt.Errorf("unsafe hardlink target for %q: %w", name, err)
			}
			if err := root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := root.RemoveAll(target); err != nil {
				return err
			}
			if err := root.Link(filepath.FromSlash(linkTarget), target); err != nil {
				return fmt.Errorf("create hardlink %q to %q: %w", name, linkTarget, err)
			}
		default:
			return fmt.Errorf("unsafe layer entry type %d for %q", header.Typeflag, name)
		}
	}
	// archive/tar may recognize the end marker without consuming every
	// trailing zero block. Drain the decompressed stream so diffID covers
	// the exact complete layer and the decompression budget covers trailing
	// data too.
	if _, err := io.Copy(io.Discard, io.TeeReader(raw, rawHash)); err != nil {
		return fmt.Errorf("finish uncompressed layer: %w", err)
	}
	if err := uncompressed.Close(); err != nil {
		return fmt.Errorf("close layer: %w", err)
	}
	if _, err := io.Copy(io.Discard, compressed); err != nil {
		return fmt.Errorf("finish layer digest: %w", err)
	}
	if info, err := file.Stat(); err != nil || info.Size() != desc.Size {
		return errors.New("layer size mismatch")
	}
	if "sha256:"+hex.EncodeToString(compressedHash.Sum(nil)) != desc.Digest {
		return errors.New("layer digest mismatch")
	}
	if "sha256:"+hex.EncodeToString(rawHash.Sum(nil)) != diffID {
		return errors.New("layer diff_id mismatch")
	}
	return nil
}

func validateSymlinkTarget(name, target string) error {
	if target == "" || strings.HasPrefix(target, "/") || path.IsAbs(target) {
		return fmt.Errorf("unsafe symlink target %q for %q", target, name)
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("symlink target %q for %q escapes rootfs", target, name)
	}
	return nil
}

func safeArchivePath(value string) (string, error) {
	clean := strings.TrimSuffix(value, "/")
	if clean == "" || strings.HasPrefix(clean, "/") || clean == ".." ||
		strings.HasPrefix(clean, "../") || path.Clean(clean) != clean || clean == "." {
		return "", fmt.Errorf("unsafe layer path %q", value)
	}
	return clean, nil
}

func applyWhiteout(root *os.Root, name string) (bool, error) {
	base := path.Base(name)
	dir := path.Dir(name)
	if base == ".wh..wh..opq" {
		target := filepath.FromSlash(dir)
		entries, err := fs.ReadDir(root.FS(), target)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if err != nil {
			return true, err
		}
		for _, entry := range entries {
			if err := root.RemoveAll(filepath.Join(target, entry.Name())); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	if strings.HasPrefix(base, ".wh.") {
		victim := strings.TrimPrefix(base, ".wh.")
		if victim == "" || victim == "." || victim == ".." {
			return true, fmt.Errorf("invalid whiteout %q", name)
		}
		return true, root.RemoveAll(filepath.FromSlash(path.Join(dir, victim)))
	}
	return false, nil
}

func normalizedMode(mode int64, directory bool) os.FileMode {
	if directory {
		// A fixed directory mode avoids host umask and archive metadata
		// producing different shared root filesystems.
		return 0o755
	}
	value := os.FileMode(mode) & 0o777
	if value == 0 {
		return 0o644
	}
	return value
}

func openBlob(layout string, desc descriptor) (*os.File, error) {
	hexDigest, err := parseDigest(desc.Digest)
	if err != nil || desc.Size < 0 {
		return nil, errors.New("invalid descriptor")
	}
	name := filepath.Join(layout, "blobs", "sha256", hexDigest)
	entry, err := os.Lstat(name)
	if err != nil || !entry.Mode().IsRegular() {
		return nil, errors.New("blob is not a regular file")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != desc.Size {
		file.Close()
		return nil, errors.New("blob is not a regular file of the declared size")
	}
	return file, nil
}

func readVerifiedBlob(layout string, desc descriptor) ([]byte, error) {
	if desc.Size > maxJSONBytes {
		return nil, errors.New("metadata blob exceeds 8 MiB")
	}
	file, err := openBlob(layout, desc)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	hash := sha256.New()
	data, err := io.ReadAll(io.TeeReader(io.LimitReader(file, desc.Size+1), hash))
	if err != nil || int64(len(data)) != desc.Size ||
		"sha256:"+hex.EncodeToString(hash.Sum(nil)) != desc.Digest {
		return nil, errors.New("descriptor digest or size mismatch")
	}
	return data, nil
}

func readJSONFile(filename string, target any) error {
	entry, err := os.Lstat(filename)
	if err != nil || !entry.Mode().IsRegular() {
		return errors.New("JSON metadata must be a regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxJSONBytes {
		return errors.New("JSON metadata must be a regular file no larger than 8 MiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxJSONBytes+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func parseDigest(value string) (string, error) {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return "", errors.New("digest must be sha256")
	}
	raw := value[7:]
	if decoded, err := hex.DecodeString(raw); err != nil || len(decoded) != sha256.Size {
		return "", errors.New("invalid sha256 digest")
	}
	return raw, nil
}

func realDirectory(value string) (string, error) {
	clean := filepath.Clean(value)
	info, err := os.Lstat(clean)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("must be a real directory")
	}
	return clean, nil
}

func digestTree(root string) (string, int, int64, error) {
	var names []string
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name != root {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	sort.Strings(names)
	hash := sha256.New()
	writer := bufio.NewWriter(hash)
	var files int
	var bytes int64
	epoch := time.Unix(0, 0)
	for _, name := range names {
		info, err := os.Lstat(name)
		if err != nil {
			return "", 0, 0, err
		}
		relative, _ := filepath.Rel(root, name)
		size := info.Size()
		if info.IsDir() {
			if err := os.Chmod(name, 0o755); err != nil {
				return "", 0, 0, err
			}
			info, err = os.Lstat(name)
			if err != nil {
				return "", 0, 0, err
			}
			size = 0
		}
		fmt.Fprintf(writer, "%s\x00%o\x00%d\x00", filepath.ToSlash(relative), info.Mode().Perm(), size)
		if info.Mode().IsRegular() {
			file, err := os.Open(name)
			if err != nil {
				return "", 0, 0, err
			}
			n, copyErr := io.Copy(writer, file)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return "", 0, 0, errors.New("read extracted file")
			}
			files++
			bytes += n
		}
		if err := os.Chtimes(name, epoch, epoch); err != nil {
			return "", 0, 0, err
		}
	}
	if err := writer.Flush(); err != nil {
		return "", 0, 0, err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), files, bytes, nil
}

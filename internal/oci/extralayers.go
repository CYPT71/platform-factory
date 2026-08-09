package oci

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// maxPrebuiltLayerBytes bounds the total uncompressed size Build will
	// accept from a single plugin-supplied layer (Options.ExtraLayers) -
	// generous enough for a real language runtime plus its installed
	// dependencies, small enough that a misbehaving or malicious plugin
	// can never exhaust host disk/memory merely by claiming a layer
	// exists.
	maxPrebuiltLayerBytes = 4 << 30 // 4 GiB
	// maxPrebuiltLayerEntries bounds entry count independently of total
	// size - a tar bomb of many empty/tiny entries would pass the size
	// check alone.
	maxPrebuiltLayerEntries = 200_000
)

// writePrebuiltLayer validates tarPath (an uncompressed tar file, as a
// language plugin's build-layer subcommand produces - see
// docs/language-plugin-layers.md) and, only if every entry passes,
// writes it into root's blob store as a new, independently-hashed OCI
// layer. Nothing about the plugin's own claims (an exit code, anything
// it printed) is trusted: Build parses and re-hashes the actual bytes
// itself, the same way every other untrusted input in this codebase is
// verified rather than believed.
func writePrebuiltLayer(root, tarPath string, level int) (descriptor, string, error) {
	file, err := os.Open(tarPath)
	if err != nil {
		return descriptor{}, "", fmt.Errorf("open: %w", err)
	}
	defer file.Close()

	blobDir := filepath.Join(root, "blobs", "sha256")
	temporary := filepath.Join(blobDir, ".layer-in-progress")
	output, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return descriptor{}, "", err
	}
	success := false
	defer func() {
		_ = output.Close()
		if !success {
			_ = os.Remove(temporary)
		}
	}()

	compressedHash := sha256.New()
	gz, err := gzip.NewWriterLevel(io.MultiWriter(output, compressedHash), level)
	if err != nil {
		return descriptor{}, "", err
	}
	gz.Header.ModTime = time.Unix(0, 0)
	gz.Header.OS = 255
	rawHash := sha256.New()
	// Every byte read from file passes through the tar parser (for
	// validation) and, via the tee, into the gzip+hash pipeline in the
	// same pass - the layer Build actually stores is bit-for-bit the
	// same stream Build validated, never a re-serialization of it.
	tr := tar.NewReader(io.TeeReader(file, io.MultiWriter(gz, rawHash)))

	var totalSize int64
	var entryCount int
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return descriptor{}, "", fmt.Errorf("read tar entry: %w", err)
		}
		entryCount++
		if entryCount > maxPrebuiltLayerEntries {
			return descriptor{}, "", fmt.Errorf("exceeds %d entries", maxPrebuiltLayerEntries)
		}
		if err := validatePrebuiltEntry(header); err != nil {
			return descriptor{}, "", err
		}
		totalSize += header.Size
		if totalSize > maxPrebuiltLayerBytes {
			return descriptor{}, "", fmt.Errorf("exceeds %d bytes", maxPrebuiltLayerBytes)
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return descriptor{}, "", fmt.Errorf("read entry %s: %w", header.Name, err)
		}
	}
	if err := gz.Close(); err != nil {
		return descriptor{}, "", err
	}
	if err := output.Close(); err != nil {
		return descriptor{}, "", err
	}
	digest := "sha256:" + hex.EncodeToString(compressedHash.Sum(nil))
	stat, err := os.Stat(temporary)
	if err != nil {
		return descriptor{}, "", err
	}
	destination := filepath.Join(blobDir, strings.TrimPrefix(digest, "sha256:"))
	if err := os.Rename(temporary, destination); err != nil {
		return descriptor{}, "", err
	}
	if err := os.Chmod(destination, 0644); err != nil {
		return descriptor{}, "", err
	}
	success = true
	diffID := "sha256:" + hex.EncodeToString(rawHash.Sum(nil))
	return descriptor{MediaType: layerMediaType, Digest: digest, Size: stat.Size()}, diffID, nil
}

// validatePrebuiltEntry rejects anything a plugin-supplied layer must
// never contain: absolute or traversing paths, anything other than a
// plain file or directory (no symlinks, hardlinks, devices, FIFOs -
// none of this project's own layers ever produce one either), and
// setuid/setgid/sticky bits.
func validatePrebuiltEntry(header *tar.Header) error {
	switch header.Typeflag {
	case tar.TypeReg, tar.TypeDir:
	default:
		return fmt.Errorf("entry %q has unsupported type %d (only regular files and directories are allowed in a plugin-supplied layer)", header.Name, header.Typeflag)
	}
	name := header.Name
	if name == "" || strings.HasPrefix(name, "/") {
		return fmt.Errorf("entry %q must be a relative path", name)
	}
	cleaned := path.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("entry %q escapes the layer root", name)
	}
	if header.Size < 0 {
		return fmt.Errorf("entry %q has a negative size", name)
	}
	const setuidSetgidSticky = 0o7000
	if header.Mode&setuidSetgidSticky != 0 {
		return fmt.Errorf("entry %q sets setuid/setgid/sticky bits, which is not allowed in a plugin-supplied layer", name)
	}
	return nil
}

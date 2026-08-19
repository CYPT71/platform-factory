// Package microvminitramfs is the application-layer service behind
// cmd/microvm-initramfs: assembling a Linux kernel initramfs natively
// from a verified local OCI image layout - converting the layout's
// rootfs with internal/rootfs.Convert, installing the project's PID 1
// and an optional fixed entrypoint, then packing the result into a
// deterministic gzip-compressed cpio archive with
// internal/rootfs.WriteInitramfs. cmd/microvm-initramfs only parses
// flags and formats the result; every actual conversion/pack/atomic-
// install step lives here, testable without going through the CLI at
// all.
package microvminitramfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/rootfs"
)

// Result is the outcome of a successful Assemble: the converted
// rootfs's identity and the final initramfs's size.
type Result struct {
	ManifestDigest string `json:"manifest_digest"`
	RootFSDigest   string `json:"rootfs_digest"`
	Files          int    `json:"files"`
	Bytes          int64  `json:"bytes"`
	InitramfsBytes int64  `json:"initramfs_bytes"`
}

// Assemble converts layout's rootfs, installs initBinary as PID 1 (plus
// an optional fixed entrypoint), and writes the resulting gzip-
// compressed cpio archive to output. output must not already exist -
// the same anti-clobber contract internal/rootfs.Convert itself
// enforces for its own output directory.
func Assemble(layout, platform, reference, initBinary string, entrypoint []string, output string) (out Result, err error) {
	if _, statErr := os.Lstat(output); !errors.Is(statErr, os.ErrNotExist) {
		if statErr == nil {
			return Result{}, errors.New("output already exists")
		}
		return Result{}, fmt.Errorf("inspect output: %w", statErr)
	}
	parent := filepath.Dir(filepath.Clean(output))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Result{}, fmt.Errorf("create output parent: %w", err)
	}

	rootDir, err := os.MkdirTemp(parent, ".platform-factory-initramfs-root-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary rootfs workspace: %w", err)
	}
	defer os.RemoveAll(rootDir)

	// Convert installs its own output atomically and refuses to overwrite
	// an existing directory, so it needs a path inside rootDir that does
	// not exist yet, rather than rootDir itself.
	converted := filepath.Join(rootDir, "rootfs")
	convertResult, err := rootfs.Convert(rootfs.Options{
		Layout: layout, Output: converted, Platform: platform, Reference: reference,
	})
	if err != nil {
		return Result{}, fmt.Errorf("convert rootfs: %w", err)
	}
	if err := rootfs.InstallInit(converted, initBinary, entrypoint); err != nil {
		return Result{}, fmt.Errorf("install init: %w", err)
	}

	temporary, err := os.CreateTemp(parent, ".platform-factory-initramfs-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary initramfs: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := rootfs.WriteInitramfs(converted, temporary); err != nil {
		_ = temporary.Close()
		return Result{}, fmt.Errorf("write initramfs: %w", err)
	}
	info, err := temporary.Stat()
	if err != nil {
		_ = temporary.Close()
		return Result{}, fmt.Errorf("stat initramfs: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Result{}, fmt.Errorf("close initramfs: %w", err)
	}
	if err := os.Rename(temporaryName, output); err != nil {
		return Result{}, fmt.Errorf("install initramfs: %w", err)
	}
	return Result{
		ManifestDigest: convertResult.ManifestDigest,
		RootFSDigest:   convertResult.RootFSDigest,
		Files:          convertResult.Files,
		Bytes:          convertResult.Bytes,
		InitramfsBytes: info.Size(),
	}, nil
}

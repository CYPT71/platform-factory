// Helpers shared by every native microVM backend's own runNativeKVM
// implementation (microvm_native_linux_amd64.go's KVM backend,
// microvm_native_darwin.go's HVF backend) - pure Go, no platform-specific
// syscalls, so this file carries no build tag at all.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func nativeLog(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "[%s] %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// findRepoRoot locates the module root the same way cmd/platform-factory-installer
// does: ask the go toolchain for the current module's go.mod, which this
// backend already depends on (it shells to `go build` for cmd/microvm-init)
// rather than assuming the caller's current directory is the repo root.
func findRepoRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("locate go.mod: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || filepath.Base(gomod) != "go.mod" {
		return "", errors.New("run platform-factory from within the secure-oci-base repository (go.mod not found)")
	}
	return filepath.Dir(gomod), nil
}

// readEntrypoint reads the manifest rootfs.Convert selected (identified by
// its already-verified digest, returned in rootfs.Result.ManifestDigest)
// and decodes its image config's Entrypoint - the one piece of image
// metadata rootfs.Convert deliberately never surfaces itself (it verifies
// and extracts filesystem content only). Mirrors run-microvm.sh's own
// config parsing: a non-empty Entrypoint is required, matching that
// script's existing contract exactly.
func readEntrypoint(layoutDir, manifestDigest string) ([]string, error) {
	manifestBytes, err := readVerifiedBlob(layoutDir, manifestDigest)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var manifestDoc struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(manifestBytes, &manifestDoc); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	configBytes, err := readVerifiedBlob(layoutDir, manifestDoc.Config.Digest)
	if err != nil {
		return nil, fmt.Errorf("read image config: %w", err)
	}
	var configDoc struct {
		Config struct {
			Entrypoint []string `json:"Entrypoint"`
		} `json:"config"`
	}
	if err := json.Unmarshal(configBytes, &configDoc); err != nil {
		return nil, fmt.Errorf("decode image config: %w", err)
	}
	if len(configDoc.Config.Entrypoint) == 0 {
		return nil, errors.New("image config has no Entrypoint")
	}
	return configDoc.Config.Entrypoint, nil
}

// readVerifiedBlob reads a content-addressed blob by its "sha256:<hex>"
// digest and confirms its content actually hashes to that digest before
// returning it - the same check rootfs.Convert already applies to every
// blob it reads, repeated here because Entrypoint extraction reads two
// blobs (manifest, image config) that Convert itself never returns the
// bytes of.
func readVerifiedBlob(layoutDir, digest string) ([]byte, error) {
	digestHex, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || digestHex == "" {
		return nil, fmt.Errorf("unsupported digest %q", digest)
	}
	data, err := os.ReadFile(filepath.Join(layoutDir, "blobs", "sha256", digestHex))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digestHex {
		return nil, fmt.Errorf("blob %s failed digest verification", digest)
	}
	return data, nil
}

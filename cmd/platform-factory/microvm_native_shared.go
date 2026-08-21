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
	"strconv"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/networking"
	"github.com/CYPT71/platform-factory/internal/rootfs"
)

func nativeLog(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "[%s] %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func installNativeRuntimeContract(convertedRoot, initBinary string, metadata rootfs.RuntimeMetadata, forwards []networking.Forward) error {
	if len(metadata.UnsupportedOptions) != 0 {
		return fmt.Errorf("OCI options cannot be translated into the native guest: %s", strings.Join(metadata.UnsupportedOptions, ", "))
	}
	if len(metadata.Volumes) != 0 {
		return fmt.Errorf("OCI volumes cannot be translated without explicit host sources: %s", strings.Join(metadata.Volumes, ", "))
	}
	for _, required := range metadata.Ports {
		number, protocol, ok := strings.Cut(required, "/")
		guestPort, err := strconv.Atoi(number)
		if !ok || err != nil {
			return fmt.Errorf("invalid OCI exposed port %q", required)
		}
		translated := false
		for _, forward := range forwards {
			if forward.GuestPort == guestPort && forward.Protocol == protocol {
				translated = true
				break
			}
		}
		if !translated {
			return fmt.Errorf("OCI exposed port %s has no matching --publish forwarding", required)
		}
	}
	if err := rootfs.InstallInit(convertedRoot, initBinary, metadata.Process.Args); err != nil {
		return fmt.Errorf("install init: %w", err)
	}
	if err := rootfs.InstallProcessConfig(convertedRoot, metadata.Process); err != nil {
		return fmt.Errorf("install OCI process config: %w", err)
	}
	return nil
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
		return "", errors.New("run platform-factory from within the platform-factory repository (go.mod not found)")
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

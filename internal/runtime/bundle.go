package runtime

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	api "github.com/CYPT71/secure-oci-base/internal/microvm"
)

// NewBootBundle creates a canonical content-addressed boot description.
func NewBootBundle(kernel, initrd, rootfs string, commandLine []string, metadata map[string]string) (api.BootBundle, error) {
	for name, digest := range map[string]string{"kernel": kernel, "rootfs": rootfs} {
		if !validDigest(digest) {
			return api.BootBundle{}, errors.New("vmm: " + name + " must be pinned by sha256 digest")
		}
	}
	if initrd != "" && !validDigest(initrd) {
		return api.BootBundle{}, errors.New("vmm: initrd must be pinned by sha256 digest")
	}
	bundle := api.BootBundle{
		APIVersion: api.APIVersion, Kernel: kernel, Initrd: initrd, RootFS: rootfs,
		CommandLine: append([]string(nil), commandLine...), Metadata: cloneSorted(metadata),
	}
	payload, err := json.Marshal(bundle)
	if err != nil {
		return api.BootBundle{}, err
	}
	sum := sha256.Sum256(payload)
	bundle.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return bundle, nil
}

// ValidateBootBundle verifies both the pinned inputs and the canonical digest
// of a boot bundle before a backend resolves or executes any of its content.
// Callers must not rely on a bundle having been produced by NewBootBundle:
// a MachineSpec may have been translated from an untrusted wire document.
func ValidateBootBundle(bundle api.BootBundle) error {
	if bundle.APIVersion != api.APIVersion {
		return fmt.Errorf("vmm: boot bundle api_version %q is unsupported (want %q)", bundle.APIVersion, api.APIVersion)
	}
	for name, digest := range map[string]string{
		"bundle": bundle.Digest,
		"kernel": bundle.Kernel,
		"rootfs": bundle.RootFS,
	} {
		if !validDigest(digest) {
			return errors.New("vmm: " + name + " must be pinned by sha256 digest")
		}
	}
	if bundle.Initrd != "" && !validDigest(bundle.Initrd) {
		return errors.New("vmm: initrd must be pinned by sha256 digest")
	}

	canonical := bundle
	canonical.Digest = ""
	canonical.CommandLine = append([]string(nil), bundle.CommandLine...)
	canonical.Metadata = cloneSorted(bundle.Metadata)
	payload, err := json.Marshal(canonical)
	if err != nil {
		return fmt.Errorf("vmm: encode canonical boot bundle: %w", err)
	}
	sum := sha256.Sum256(payload)
	expected := "sha256:" + hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(bundle.Digest), []byte(expected)) != 1 {
		return fmt.Errorf("vmm: boot bundle digest mismatch: got %s, want %s", bundle.Digest, expected)
	}
	return nil
}

func cloneSorted(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make(map[string]string, len(values))
	for _, key := range keys {
		result[key] = values[key]
	}
	return result
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

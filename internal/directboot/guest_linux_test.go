//go:build linux && amd64

package directboot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/CYPT71/secure-oci-base/internal/microvm"
)

func TestRunWithGuestAgentRequiresProvisionedPinnedInitramfsBeforeKVM(t *testing.T) {
	kernel := []byte("pinned test kernel")
	path := filepath.Join(t.TempDir(), "kernel")
	if err := os.WriteFile(path, kernel, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(kernel)
	callbackCalled := false
	_, err := RunWithGuestAgent(context.Background(), Config{
		KernelPath:   path,
		KernelDigest: "sha256:" + hex.EncodeToString(sum[:]),
		MemoryMiB:    128,
		VCPUs:        1,
	}, GuestAgentOptions{
		SessionKey: bytes.Repeat([]byte{1}, 32),
		OnReady: func(api.GuestAgent) error {
			callbackCalled = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "pre-provisioned initramfs") {
		t.Fatalf("error=%v", err)
	}
	if callbackCalled {
		t.Fatal("agent published before provisioned initramfs validation")
	}
}

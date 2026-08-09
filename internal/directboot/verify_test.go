package directboot

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPinnedVerifiesContentAndRejectsUnsafeInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kernel")
	data := []byte("kernel")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	got, err := readPinned(path, digest, "kernel", true)
	if err != nil || string(got) != string(data) {
		t.Fatalf("got=%q err=%v", got, err)
	}
	for name, candidate := range map[string][2]string{
		"missing path":    {"", digest},
		"missing digest":  {path, ""},
		"digest mismatch": {path, "sha256:" + strings.Repeat("0", 64)},
		"missing file":    {path + ".missing", digest},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readPinned(candidate[0], candidate[1], "kernel", true); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
	if optional, err := readPinned("", "", "initramfs", false); err != nil || optional != nil {
		t.Fatalf("optional=%v err=%v", optional, err)
	}
}

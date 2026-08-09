package directboot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	KernelPath      string
	KernelDigest    string
	InitramfsPath   string
	InitramfsDigest string
	CommandLine     string
	MemoryMiB       uint64
	VCPUs           uint32
}

type Result struct {
	Serial []byte
}

func readPinned(path, digest, label string, required bool) ([]byte, error) {
	if path == "" && !required {
		return nil, nil
	}
	if path == "" || !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return nil, fmt.Errorf("direct boot: %s path and sha256 digest are required", label)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("direct boot: %s must be a real regular file", label)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != digest {
		return nil, fmt.Errorf("direct boot: %s digest mismatch: got %s", label, actual)
	}
	if len(data) == 0 {
		return nil, errors.New("direct boot: pinned file is empty")
	}
	return data, nil
}

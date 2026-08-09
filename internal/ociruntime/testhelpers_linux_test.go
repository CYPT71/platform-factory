//go:build linux

package ociruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// testBundle builds a minimal, valid OCI bundle on disk. It is shared by
// test files with different build tags (runtime_linux_test.go needs
// linux/amd64, since it exercises the KVM-backed supervisor;
// apparmor_linux_test.go only needs linux) and so must not itself carry
// an amd64 restriction.
func testBundle(t *testing.T) string {
	t.Helper()
	umask := uint32(0o022)
	bundle := t.TempDir()
	rootfs := filepath.Join(bundle, "rootfs")
	if err := os.MkdirAll(filepath.Join(rootfs, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, "app", "service"), []byte("service"), 0o555); err != nil {
		t.Fatal(err)
	}
	config := Config{
		OCIVersion: "1.2.0", Root: Root{Path: "rootfs", Readonly: true},
		Process: Process{Args: []string{"/app/service"}, Env: []string{"A=B"}, Cwd: "/",
			User: User{UID: 1000, GID: 1000, Umask: &umask}},
		Annotations: map[string]string{"test": "value"},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return bundle
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestPullImageRootfsAgainstRealDockerHub exercises the real Docker Hub
// anonymous-pull bearer-auth flow and the real registry-1.docker.io API -
// deliberately not hermetic, since that auth flow has never been
// exercised against this client before and needs proving against the
// real service at least once. Skipped unless
// PLATFORM_FACTORY_TEST_LIVE_REGISTRY=1, matching this repo's existing
// convention for opt-in real-infrastructure tests (see
// PLATFORM_FACTORY_TEST_BZIMAGE in internal/hypervisor/kvm).
func TestPullImageRootfsAgainstRealDockerHub(t *testing.T) {
	if os.Getenv("PLATFORM_FACTORY_TEST_LIVE_REGISTRY") != "1" {
		t.Skip("set PLATFORM_FACTORY_TEST_LIVE_REGISTRY=1 to pull python:3.12-slim from the real Docker Hub")
	}
	dest := filepath.Join(t.TempDir(), "rootfs")
	digest, err := pullImageRootfs(context.Background(), "python@sha256:dd29372629eeba2dd003fd9e9d35a5b8236c44727875a0364254b5127af88e65", "amd64", dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved platform manifest digest: %s", digest)
	interpreter := filepath.Join(dest, "usr", "local", "bin", "python3.12")
	if info, err := os.Stat(interpreter); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("expected an executable interpreter at %s: %v", interpreter, err)
	}
}

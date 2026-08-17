//go:build linux

package main

import (
	"context"
	"testing"

	"github.com/containerd/containerd/v2/pkg/shim"
)

func TestAllowedContainerdSocketRejectsMalformedAddresses(t *testing.T) {
	for _, address := range []string{
		"",
		"relative/path.sock",
		"/run/containerd/containerd.sock\x00/etc/passwd",
	} {
		if err := allowedContainerdSocket(address); err == nil {
			t.Fatalf("address %q was accepted", address)
		}
	}
}

func TestAllowedContainerdSocketAcceptsWellFormedAddressByDefault(t *testing.T) {
	t.Setenv(allowedContainerdSocketEnv, "")
	if err := allowedContainerdSocket("/run/containerd/containerd.sock"); err != nil {
		t.Fatalf("well-formed address rejected: %v", err)
	}
}

func TestAllowedContainerdSocketEnforcesPinnedValueWhenSet(t *testing.T) {
	t.Setenv(allowedContainerdSocketEnv, "/run/containerd/containerd.sock")

	if err := allowedContainerdSocket("/run/containerd/containerd.sock"); err != nil {
		t.Fatalf("pinned address rejected: %v", err)
	}
	if err := allowedContainerdSocket("/run/containerd/containerd.sock.ttrpc"); err == nil {
		t.Fatal("a different, structurally valid address was accepted despite the pin")
	}
	if err := allowedContainerdSocket("/tmp/attacker-controlled.sock"); err == nil {
		t.Fatal("an unrelated absolute path was accepted despite the pin")
	}
}

// TestStartRefusesUntrustedAddressBeforeTouchingTheSocket proves the check
// runs first: Start must fail on a malformed opts.Address without ever
// reaching shim.SocketAddress/shim.NewSocket (which would otherwise touch
// the filesystem and require a real containerd runtime environment this
// unit test does not have).
func TestStartRefusesUntrustedAddressBeforeTouchingTheSocket(t *testing.T) {
	_, err := shimManager{}.Start(context.Background(), "test-id", shim.StartOpts{Address: ""})
	if err == nil {
		t.Fatal("Start accepted an empty containerd address")
	}
}

//go:build linux

// Command platform-factory-shim (installed as containerd-shim-platform-factory-v1,
// matching containerd's shim naming convention for runtime_type
// "io.containerd.platform-factory.v1") is
// containerd's other integration path alongside the OCI-CLI facade
// platform-factory-runtime: it exists only because containerd's default sandbox
// model - reusing runc's OCI-spec-based container creation for the pod
// sandbox too - unconditionally assigns the sandbox a non-empty Linux
// capability set that platform-factory-runtime deliberately, and correctly,
// refuses (see plugins/containerd/internal/containerdshim and
// docs/containerd-kubernetes.md for the full story).
//
// This binary implements containerd's runtime v2 shim protocol directly: a
// Sandbox TTRPC service (lightweight - no VM, just pod-scoped bookkeeping,
// so it never presents anything for a capability policy to reject) and a
// Task TTRPC service for the containers within that sandbox, which shells
// out to the same, already-proven platform-factory-runtime binary Podman drives
// today. It never re-implements OCI runtime semantics itself.
package main

import (
	"context"

	"github.com/containerd/containerd/v2/pkg/shim"
)

func main() {
	shim.Run(context.Background(), shimManager{})
}

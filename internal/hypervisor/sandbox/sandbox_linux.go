//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	capSysAdmin = 21
	capSetPCap  = 8
)

// applyNamespaces, applyCgroups and dropCapability - the real,
// per-syscall implementations this file's ProbeSandbox/hasCapability
// probes are about - live in namespace_linux.go, cgroup_linux.go and
// caps_linux.go respectively. That split keeps this file to what's
// unique to it: reporting availability without mutating process state
// (hasCapability, ProbeSandbox) and the cgroup-path lookup both this
// file's probe and cgroup_linux.go's applyCgroups need.

// applySeccomp sets PR_SET_NO_NEW_PRIVS on the calling process. Real BPF
// syscall filtering is a separate, deliberately harder-to-misuse primitive
// - applyStrictSeccomp (seccomp_linux.go) - specifically because this one
// needs to stay safe to call from anywhere, unconditionally, without the
// caller having to reason about which OS thread it lands on: this
// codebase's other sandboxed process (internal/executor) uses the same
// PR_SET_NO_NEW_PRIVS-only primitive under the same name, and this
// package's own applyVMMSandbox caller (internal/ociruntime) calls it
// before the VMM host process has pinned itself to an OS thread at all.
// It closes the privilege-escalation path that matters most for a process
// that never execs untrusted binaries: a compromised process can no
// longer gain capabilities by exec'ing a setuid or file-capability binary.
// This call requires no privilege and cannot fail for lack of it.
func (s *Sandbox) applySeccomp() error {
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", errno)
	}
	return nil
}

// ownCgroupV2Dir resolves the calling process's own cgroup v2
// directory from /proc/self/cgroup, mirroring
// internal/executor/sandbox_linux.go's helper of the same name (a
// different package, so it cannot be shared directly).
func ownCgroupV2Dir() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" && parts[1] == "" {
			dir := filepath.Join("/sys/fs/cgroup", parts[2])
			if _, err := os.Stat(filepath.Join(dir, "cgroup.controllers")); err == nil {
				return dir, nil
			}
			return "", fmt.Errorf("cgroup v2 path %s has no cgroup.controllers (v1 hierarchy or hybrid host)", dir)
		}
	}
	return "", fmt.Errorf("no cgroup v2 entry in /proc/self/cgroup (host uses cgroup v1)")
}

// hasCapability reports whether the calling process currently holds
// bit in its effective capability set, read from /proc/self/status.
// This never mutates process state, unlike actually attempting the
// operations it predicts the outcome of.
func hasCapability(bit uint) bool {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		hex, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(hex), 16, 64)
		if err != nil {
			return false
		}
		return mask&(1<<bit) != 0
	}
	return false
}

// ProbeSandbox reports, by construction rather than by guessing from
// euid, which of this package's real sandboxing operations the
// current process can actually perform. Namespace and capability
// checks read the process's own effective capability set rather than
// attempting the operation directly, because unshare(2) cannot be
// undone on this process without the very capability being probed
// for. The cgroup check does attempt (and clean up) a real, harmless
// child-directory creation, mirroring
// internal/executor.ProbeSandbox's philosophy for that facility.
func ProbeSandbox() Support {
	support := Support{Details: map[string]string{}}

	if hasCapability(capSysAdmin) {
		support.Namespaces = true
	} else {
		support.Details["namespaces"] = "CAP_SYS_ADMIN not held; unshare(CLONE_NEWUTS|CLONE_NEWIPC) would fail"
	}

	if hasCapability(capSetPCap) {
		support.CapabilityBoundingDrop = true
	} else {
		support.Details["capability-bounding-drop"] = "CAP_SETPCAP not held; PR_CAPBSET_DROP would fail"
	}

	dir, err := ownCgroupV2Dir()
	if err != nil {
		support.Details["cgroups"] = err.Error()
		return support
	}
	probeDir := filepath.Join(dir, "platform-factory-sandbox-probe")
	if err := os.Mkdir(probeDir, 0o755); err != nil {
		support.Details["cgroups"] = "cannot create a child cgroup (run under a delegated cgroup or as root): " + err.Error()
		return support
	}
	defer func() { _ = os.Remove(probeDir) }()
	if os.WriteFile(filepath.Join(probeDir, "pids.max"), []byte("max"), 0o644) == nil {
		support.Cgroups = true
	} else {
		support.Details["cgroups"] = "pids controller is not delegated to child cgroups"
	}
	return support
}

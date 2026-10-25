//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// A regular filesystem fixture verifies the controller file contract without
// moving the test process into a real kernel cgroup or requiring delegation.
func TestCgroupControllerWritesUseExplicitParentAndConfiguredLimits(t *testing.T) {
	parent := t.TempDir()
	s := &Sandbox{config: Config{CgroupParent: parent, CPULimit: 50, MemoryLimit: 1048576, PIDsLimit: 10}}
	if err := s.applyCgroups(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(s.cgroupPath) != parent || s.cgroupParent != parent {
		t.Fatal("cgroup escaped configured parent")
	}
	for name, want := range map[string]string{"cpu.max": "50000 100000", "memory.max": "1048576", "pids.max": "10", "cgroup.procs": strconv.Itoa(os.Getpid())} {
		path := filepath.Join(s.cgroupPath, name)
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("%s=%q want %q: %v", name, raw, want, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	leaf := s.cgroupPath
	if err := removeCgroup(leaf, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leaf); !os.IsNotExist(err) {
		t.Fatal("leaf not removed")
	}
	if err := removeCgroup(leaf, ""); err != nil {
		t.Fatal("missing leaf cleanup not idempotent")
	}
}
func TestCgroupControllerFailuresAreExplicit(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "missing")
	s := &Sandbox{config: Config{CgroupParent: parent}}
	if err := s.applyCgroups(); err == nil {
		t.Fatal("missing parent accepted")
	}
	if err := writeCgroupFile(parent, "memory.max", "1"); err == nil {
		t.Fatal("failed controller write hidden")
	}
	if err := removeCgroup("unused", parent); err == nil {
		t.Fatal("failed parent reattachment hidden")
	}
}

func TestNamespacePolicyRejectsUnsupportedIsolationBeforeAnyUnshare(t *testing.T) {
	for _, ns := range []Namespace{NamespacePID, NamespaceUSER, NamespaceMNT, Namespace("unknown")} {
		s := &Sandbox{config: Config{NamespaceList: []Namespace{NamespaceNET, NamespaceUTS, NamespaceIPC, ns}}}
		if err := s.applyNamespaces(); err == nil {
			t.Fatalf("unsupported namespace %q accepted", ns)
		}
	}
	if path, err := currentCgroupV2Path(); err == nil && path == "" {
		t.Fatal("empty current cgroup directory")
	}
}

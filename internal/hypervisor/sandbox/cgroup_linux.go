//go:build linux

package sandbox

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cgroupRoot is where the kernel expects the cgroup v2 unified hierarchy to
// be mounted; there is exactly one of these on a cgroup-v2 host, unlike v1's
// per-controller mount points.
const cgroupRoot = "/sys/fs/cgroup"

// currentCgroupV2Path returns this process's own cgroup v2 directory,
// parsed from /proc/self/cgroup. On a cgroup-v2-only host (what this
// package requires - see applyCgroups) that file has exactly one line,
// always prefixed "0::".
func currentCgroupV2Path() (string, error) {
	f, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if rel, ok := strings.CutPrefix(line, "0::"); ok {
			return filepath.Join(cgroupRoot, rel), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	return "", fmt.Errorf("no cgroup v2 (\"0::\") entry in /proc/self/cgroup - host is not cgroup-v2-only")
}

// applyCgroups creates a leaf cgroup v2 directory under s.config.CgroupParent
// (or this process's own current cgroup, if that's unset), applies the
// configured CPU/memory/PIDs limits to it, and moves the calling process
// into it. It requires write access to the parent directory, which in a
// real deployment means the cgroup subtree has already been delegated to
// whatever user/service account runs the VMM supervisor - the same
// precondition patch.bash's own check_kvm documents for /dev/kvm access.
func (s *Sandbox) applyCgroups() error {
	parent := s.config.CgroupParent
	if parent == "" {
		var err error
		parent, err = currentCgroupV2Path()
		if err != nil {
			return err
		}
	}
	leaf := filepath.Join(parent, fmt.Sprintf("platform-factory-vmm-%d", os.Getpid()))
	if err := os.Mkdir(leaf, 0o755); err != nil {
		return fmt.Errorf("create cgroup %s: %w", leaf, err)
	}
	// Recorded now, before any limit write below can fail, so a partial
	// failure still leaves Cleanup able to find and remove this leaf
	// instead of leaking an empty, untracked cgroup directory.
	s.cgroupPath = leaf
	s.cgroupParent = parent

	if s.config.CPULimit > 0 {
		// cgroup v2 cpu.max: "<quota> <period>", both in microseconds.
		// CPULimit is documented (Config.CPULimit) as a percentage of
		// one core, so quota = CPULimit% of a fixed 100ms period.
		const periodMicros = 100000
		quota := int64(s.config.CPULimit) * periodMicros / 100
		if err := writeCgroupFile(leaf, "cpu.max", fmt.Sprintf("%d %d", quota, periodMicros)); err != nil {
			return err
		}
	}
	if s.config.MemoryLimit > 0 {
		if err := writeCgroupFile(leaf, "memory.max", strconv.FormatInt(s.config.MemoryLimit, 10)); err != nil {
			return err
		}
	}
	if s.config.PIDsLimit > 0 {
		if err := writeCgroupFile(leaf, "pids.max", strconv.Itoa(s.config.PIDsLimit)); err != nil {
			return err
		}
	}
	if err := writeCgroupFile(leaf, "cgroup.procs", strconv.Itoa(os.Getpid())); err != nil {
		return err
	}
	return nil
}

func writeCgroupFile(dir, name, value string) error {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(value), 0); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// removeCgroup moves the calling process back into parent (a cgroup can't
// be removed while it still contains a live process) and then removes
// path. Called from Sandbox.Cleanup, which is expected to run just before
// this process exits - if it doesn't, and this process is still inside
// path when some other caller removes it another way, that's a caller
// ordering bug, not something this function can fix.
func removeCgroup(path, parent string) error {
	if parent != "" {
		if err := writeCgroupFile(parent, "cgroup.procs", strconv.Itoa(os.Getpid())); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove cgroup %s: %w", path, err)
	}
	return nil
}

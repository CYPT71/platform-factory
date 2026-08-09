//go:build linux

package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
)

var stageCgroupCounter atomic.Int64

// stageCgroup is one per-stage child cgroup with pids/cpu limits
// applied before the stage process starts.
type stageCgroup struct {
	dir  string
	file *os.File
}

// newStageCgroup creates a child cgroup under this process's own
// cgroup v2 directory, writes the requested limits and attaches it to
// cmd via clone-into-cgroup, so the stage starts inside its limits
// rather than being moved after the fact.
func newStageCgroup(cgroupDir string, cmd *exec.Cmd, pids, cpuMilli int64) (*stageCgroup, error) {
	// The controllers are enforced through a child cgroup that the stage
	// process is clone'd directly into. ProbeSandbox has already confirmed,
	// by writing the very interface file used below in a throwaway child,
	// that the requested controller is delegated to children here; it does
	// not try to enable delegation itself, because enabling a domain
	// controller in a cgroup that still holds member processes leaves that
	// cgroup unable to accept a clone-into-cgroup child (EOPNOTSUPP).
	dir := filepath.Join(cgroupDir,
		fmt.Sprintf("platform-factory-stage-%d-%d", os.Getpid(), stageCgroupCounter.Add(1)))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create stage cgroup: %w", err)
	}
	group := &stageCgroup{dir: dir}
	if pids > 0 {
		if err := os.WriteFile(filepath.Join(dir, "pids.max"), []byte(strconv.FormatInt(pids, 10)), 0o644); err != nil {
			group.cleanup()
			return nil, fmt.Errorf("write pids.max: %w", err)
		}
	}
	if cpuMilli > 0 {
		// CPUMilli is a millicore share: quota = milli * period / 1000.
		const period = 100000
		quota := cpuMilli * period / 1000
		if err := os.WriteFile(filepath.Join(dir, "cpu.max"),
			fmt.Appendf(nil, "%d %d", quota, period), 0o644); err != nil {
			group.cleanup()
			return nil, fmt.Errorf("write cpu.max: %w", err)
		}
	}
	file, err := os.Open(dir)
	if err != nil {
		group.cleanup()
		return nil, fmt.Errorf("open stage cgroup: %w", err)
	}
	group.file = file
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(file.Fd())
	return group, nil
}

func (g *stageCgroup) cleanup() {
	if g.file != nil {
		_ = g.file.Close()
	}
	_ = os.Remove(g.dir)
}

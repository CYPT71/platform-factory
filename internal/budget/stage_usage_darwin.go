//go:build darwin

package budget

import (
	"os"
	"syscall"
	"time"
)

// FromProcessState extracts a completed child process's total CPU
// time (user+system) and peak resident set size from its
// *os.ProcessState, as returned by (*exec.Cmd).Wait() once the
// process has exited. Darwin's getrusage(2) reports ru_maxrss in
// bytes, unlike Linux's kilobytes - see stage_usage_linux.go and
// `man getrusage`'s per-OS NOTES section, not guessed.
func FromProcessState(state *os.ProcessState) StageUsage {
	if state == nil {
		return StageUsage{}
	}
	rusage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || rusage == nil {
		return StageUsage{}
	}
	cpu := time.Duration(rusage.Utime.Sec+rusage.Stime.Sec)*time.Second +
		time.Duration(rusage.Utime.Usec+rusage.Stime.Usec)*time.Microsecond
	return StageUsage{CPU: cpu, PeakRSS: rusage.Maxrss}
}

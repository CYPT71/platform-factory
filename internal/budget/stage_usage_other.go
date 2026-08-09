//go:build !linux && !darwin

package budget

import "os"

// FromProcessState returns the zero value on platforms where
// *os.ProcessState does not expose per-child rusage through a
// syscall.Rusage-shaped SysUsage() (Windows' ProcessState.SysUsage()
// returns a different concrete type). A caller that wants CPU/memory
// budgeting on such a platform needs a different source; this is a
// documented "unavailable", not a silent wrong answer.
func FromProcessState(state *os.ProcessState) StageUsage {
	return StageUsage{}
}

//go:build windows

package budget

import (
	"syscall"
	"time"
)

// cpuTime returns the current process's user-mode CPU time via
// GetProcessTimes, Windows' equivalent of Unix's RUSAGE_SELF user time.
func cpuTime() time.Duration {
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var creationTime, exitTime, kernelTime, userTime syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &creationTime, &exitTime, &kernelTime, &userTime); err != nil {
		return 0
	}
	// Filetime is a count of 100-nanosecond intervals.
	hundredNanoseconds := int64(userTime.HighDateTime)<<32 | int64(userTime.LowDateTime)
	return time.Duration(hundredNanoseconds * 100)
}

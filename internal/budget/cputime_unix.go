//go:build !windows

package budget

import (
	"syscall"
	"time"
)

// cpuTime returns the current process CPU time.
func cpuTime() time.Duration {
	var rusage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &rusage); err == nil {
		return time.Duration(rusage.Utime.Sec)*time.Second + time.Duration(rusage.Utime.Usec)*time.Microsecond
	}
	// Fallback: use runtime if syscall not available
	return time.Duration(0)
}

//go:build windows

package sandbox

import "fmt"

// setuid has no Windows equivalent: there is no POSIX UID to drop.
func setuid(uid int) error {
	return fmt.Errorf("sandbox: setuid(%d) is not supported on windows", uid)
}

// closeFD is a no-op on Windows: this package's file descriptors
// (seccomp filter, namespace FDs) are Unix-only concepts to begin with,
// so nothing was ever opened here to close.
func closeFD(fd int) {}

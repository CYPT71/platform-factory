//go:build !windows

package sandbox

import "syscall"

// setuid drops the calling process's UID to uid.
func setuid(uid int) error { return syscall.Setuid(uid) }

// closeFD closes a raw file descriptor obtained from a Unix syscall
// (seccomp filter FD, namespace FD).
func closeFD(fd int) { _ = syscall.Close(fd) }

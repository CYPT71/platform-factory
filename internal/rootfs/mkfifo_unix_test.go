//go:build !windows

package rootfs

import "syscall"

// mkfifo creates a named pipe for TestWriteInitramfsRejectsUnsupportedEntryType.
func mkfifo(path string, mode uint32) error { return syscall.Mkfifo(path, mode) }

//go:build windows

package rootfs

import "errors"

// mkfifo has no Windows equivalent. TestWriteInitramfsRejectsUnsupportedEntryType
// skips itself on Windows before ever calling this; it exists only so the
// package compiles here too.
func mkfifo(path string, mode uint32) error {
	return errors.New("rootfs: mkfifo is not supported on windows")
}

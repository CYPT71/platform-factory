//go:build !darwin

package main

import (
	"os"
	"time"
)

// fileBirthTime falls back to info's modification time on platforms
// without a portable creation-time syscall exposed via os.FileInfo (Linux's
// real answer, statx's stx_btime, isn't reachable through the standard
// library). Still fully reproducible run to run, as long as the file
// itself isn't rewritten between builds - just not a true "birth" time.
func fileBirthTime(info os.FileInfo) time.Time {
	return info.ModTime()
}

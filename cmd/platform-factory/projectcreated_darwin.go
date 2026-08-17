//go:build darwin

package main

import (
	"os"
	"syscall"
	"time"
)

// fileBirthTime returns info's real filesystem creation time on Darwin
// (APFS/HFS+ report this natively via Stat_t.Birthtimespec), falling back
// to its modification time if the underlying Sys() value isn't the type
// we expect.
func fileBirthTime(info os.FileInfo) time.Time {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.ModTime()
	}
	return time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec)
}

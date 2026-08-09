//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package plugin

import (
	"errors"
	"os"
)

func openPluginExecutable(string, string) (*os.File, error) {
	return nil, errors.New("atomic symlink-free plugin executable opening is unavailable on this platform")
}

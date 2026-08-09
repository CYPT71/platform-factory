//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openPluginExecutable walks from a pinned directory descriptor. Every
// component is opened relative to the previously opened descriptor with
// O_NOFOLLOW, so renames cannot redirect traversal and symlinks are never
// followed between validation and use.
func openPluginExecutable(dir, relative string) (*os.File, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, errors.New("unsafe relative executable path")
	}
	rootFD, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open pinned plugin directory: %w", err)
	}
	currentFD := rootFD
	closeCurrent := func() { _ = unix.Close(currentFD) }
	afterPluginRootOpen()
	components := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	for _, component := range components[:len(components)-1] {
		if component == "" || component == "." || component == ".." {
			closeCurrent()
			return nil, errors.New("unsafe executable path component")
		}
		nextFD, openErr := unix.Openat(currentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		closeCurrent()
		if openErr != nil {
			return nil, fmt.Errorf("open executable parent %q without symlinks: %w", component, openErr)
		}
		currentFD = nextFD
	}
	name := components[len(components)-1]
	if name == "" || name == "." || name == ".." {
		closeCurrent()
		return nil, errors.New("unsafe executable filename")
	}
	fileFD, err := unix.Openat(currentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	closeCurrent()
	if err != nil {
		return nil, fmt.Errorf("open executable %q without symlinks: %w", name, err)
	}
	file := os.NewFile(uintptr(fileFD), relative)
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, errors.New("wrap executable descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat executable descriptor: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("executable descriptor is not a regular file")
	}
	return file, nil
}

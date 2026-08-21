package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/internal/vmdisk"
)

// scanLegacyDiskCandidates looks at dir's own top-level regular files
// (never recursing into subdirectories, and never touching anything
// outside dir) and returns the ones internal/vmdisk positively
// identifies as a disk image. A file that isn't a disk at all simply
// fails vmdisk.Detect and is silently skipped - this is a best-effort
// heuristic scan of a project directory, not a claim that every
// returned path is definitely meant to be booted.
func scanLegacyDiskCandidates(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scan %s for legacy disks: %w", dir, err)
	}
	var candidates []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if _, err := vmdisk.Detect(path); err == nil {
			candidates = append(candidates, path)
		}
	}
	return candidates, nil
}

// detectAndResolveLegacyDisks scans dir for legacy VM disks and, if any
// are found, resolves which one is the boot disk: via bootDiskOverride
// if given, automatically if exactly one carries positive boot
// evidence, or otherwise by asking on stdin/stdout. assumeYes and a nil
// stdin both disable prompting - fails closed with a --boot-disk hint
// instead. Returns nil, nil when no disks are found at all.
func detectAndResolveLegacyDisks(dir, bootDiskOverride string, assumeYes bool, stdin *bufio.Reader, stdout, stderr io.Writer) (*project.LegacyDiskConfig, error) {
	candidates, err := scanLegacyDiskCandidates(dir)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	override := bootDiskOverride
	if override != "" {
		if abs, err := filepath.Abs(override); err == nil {
			override = abs
		}
	}

	bootIndex, disks, err := vmdisk.SelectBootDisk(candidates, override)
	fmt.Fprintf(stdout, "platform-factory init: found %d legacy VM disk(s) in %s:\n", len(disks), dir)
	for i, disk := range disks {
		if disk.BootScan != nil {
			fmt.Fprintf(stdout, "  [%d] %s: format=%s bootable=%v (%s)\n", i, disk.Path, disk.Format, disk.BootScan.Bootable, disk.BootScan.Evidence)
		} else {
			fmt.Fprintf(stdout, "  [%d] %s: format=%s boot-scan unavailable: %s\n", i, disk.Path, disk.Format, disk.ScanError)
		}
	}
	if err == nil {
		return legacyDiskConfigFor(dir, disks, bootIndex), nil
	}
	if !isVMDiskAmbiguity(err) {
		return nil, err
	}

	promptReader := stdin
	if assumeYes {
		promptReader = nil
	}
	chosen, promptErr := promptForBootDisk(disks, promptReader, stdout)
	if promptErr != nil {
		return nil, fmt.Errorf("%w (%v)", err, promptErr)
	}
	fmt.Fprintf(stdout, "platform-factory init: using %s as the boot disk\n", disks[chosen].Path)
	return legacyDiskConfigFor(dir, disks, chosen), nil
}

// isVMDiskAmbiguity reports whether err is SelectBootDisk's "I can't
// decide automatically" error, which - unlike a real I/O or format
// error - is exactly the condition an interactive prompt can resolve.
// vmdisk.SelectBootDisk doesn't export a sentinel for this today, so
// this matches on its documented, stable message prefix.
func isVMDiskAmbiguity(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "vmdisk: cannot determine the boot disk automatically")
}

func legacyDiskConfigFor(dir string, disks []vmdisk.DiskInfo, bootIndex int) *project.LegacyDiskConfig {
	config := &project.LegacyDiskConfig{Boot: relativeTo(dir, disks[bootIndex].Path), Strategy: "unsupported"}
	for i, disk := range disks {
		if i == bootIndex {
			continue
		}
		config.Data = append(config.Data, relativeTo(dir, disk.Path))
	}
	return config
}

func relativeTo(dir, path string) string {
	if rel, err := filepath.Rel(dir, path); err == nil {
		return rel
	}
	return path
}

// promptForBootDisk asks the user, on stdout, to pick one of disks by
// index, reading their answer from stdin. Returns an error (never
// panics) if stdin is nil, closed, or the answer doesn't parse to a
// valid index - callers are expected to fail the whole command closed
// in that case, exactly as if no prompt had been possible at all.
func promptForBootDisk(disks []vmdisk.DiskInfo, stdin *bufio.Reader, stdout io.Writer) (int, error) {
	if stdin == nil {
		return 0, errors.New("no interactive input available; pass --boot-disk explicitly")
	}
	fmt.Fprintf(stdout, "platform-factory init: enter the number of the boot disk [0-%d]: ", len(disks)-1)
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return 0, fmt.Errorf("read boot disk selection: %w", err)
	}
	line = strings.TrimSpace(line)
	index, err := strconv.Atoi(line)
	if err != nil || index < 0 || index >= len(disks) {
		return 0, fmt.Errorf("%q is not a valid disk number 0-%d", line, len(disks)-1)
	}
	return index, nil
}

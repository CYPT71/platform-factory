package vmdisk

import (
	"fmt"
	"strings"
)

// DiskInfo describes one disk in a multi-disk SelectBootDisk call: its
// identified container format, and - when this package could safely
// resolve the format's logical block mapping - whether it carries
// positive boot-partition evidence.
type DiskInfo struct {
	Info

	// BootScan is nil when this disk's format could not be safely
	// mapped for a partition-table read (see ScanBootScanError).
	BootScan *BootScan
	// ScanError explains why BootScan is nil, when it is.
	ScanError string
}

// SelectBootDisk identifies every disk in paths and determines which
// one is the boot/OS disk.
//
// If bootDiskOverride is non-empty it must exactly match one of paths;
// it is then used as-is, with no automatic decision made - the caller
// gets to be certain rather than trust a heuristic.
//
// Otherwise, SelectBootDisk only returns a decision when exactly one
// disk carries positive boot-partition evidence (an MBR active
// partition, a GPT ESP, or a GPT Legacy BIOS Bootable attribute). Zero
// matches, more than one match, or any disk whose format could not be
// safely scanned all fail closed with a descriptive error demanding
// --boot-disk - this package never guesses which disk to boot.
func SelectBootDisk(paths []string, bootDiskOverride string) (bootIndex int, disks []DiskInfo, err error) {
	if len(paths) == 0 {
		return 0, nil, fmt.Errorf("vmdisk: at least one disk is required")
	}
	disks = make([]DiskInfo, len(paths))
	for i, path := range paths {
		info, detectErr := Detect(path)
		if detectErr != nil {
			return 0, nil, fmt.Errorf("disk %d (%s): %w", i, path, detectErr)
		}
		disks[i].Info = info
		scan, scanErr := ScanBootPartition(path, info.Format)
		if scanErr != nil {
			disks[i].ScanError = scanErr.Error()
			continue
		}
		disks[i].BootScan = &scan
	}

	if bootDiskOverride != "" {
		for i, path := range paths {
			if path == bootDiskOverride {
				return i, disks, nil
			}
		}
		return 0, disks, fmt.Errorf("vmdisk: --boot-disk %q does not match any of the given disks", bootDiskOverride)
	}

	if len(paths) == 1 {
		// With only one disk given there is no actual choice to make -
		// scan evidence (or the lack of it) is informational only.
		return 0, disks, nil
	}

	var bootable []int
	for i, disk := range disks {
		if disk.BootScan != nil && disk.BootScan.Bootable {
			bootable = append(bootable, i)
		}
	}
	if len(bootable) == 1 {
		return bootable[0], disks, nil
	}

	return 0, disks, fmt.Errorf("vmdisk: cannot determine the boot disk automatically (%s) - pass --boot-disk explicitly", ambiguityReason(disks, bootable))
}

func ambiguityReason(disks []DiskInfo, bootable []int) string {
	var unscannable []string
	for _, d := range disks {
		if d.BootScan == nil {
			unscannable = append(unscannable, fmt.Sprintf("%s: %s", d.Path, d.ScanError))
		}
	}
	switch {
	case len(bootable) > 1:
		names := make([]string, len(bootable))
		for i, idx := range bootable {
			names[i] = disks[idx].Path
		}
		return fmt.Sprintf("%d disks carry boot-partition evidence: %s", len(bootable), strings.Join(names, ", "))
	case len(unscannable) > 0:
		return fmt.Sprintf("%d disk(s) could not be scanned: %s", len(unscannable), strings.Join(unscannable, "; "))
	default:
		return "no disk carries boot-partition evidence"
	}
}

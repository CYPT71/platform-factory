package vmdisk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// unknownOperatingSystem distinguishes unsupported inspection from no OS found.
const unknownOperatingSystem = "unknown: filesystem inspection is not implemented yet (internal/vmdisk identifies disk containers and partition tables only)"

// DiscoveryReport contains bounded header and partition-table evidence.
// Unsupported inventory dimensions remain explicit and empty.
type DiscoveryReport struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Disks       []DiskDiscovery `json:"disks"`

	// BootDiskResolved and BootDiskIndex mirror SelectBootDisk's own
	// decision - false/zero when no single disk could be determined
	// automatically (see HumanReviewItems for why).
	BootDiskResolved bool `json:"boot_disk_resolved"`
	BootDiskIndex    int  `json:"boot_disk_index"`

	// Limitations states, in the report itself, exactly what this
	// analysis pass can and cannot see - required reading before
	// treating an empty inventory field as "clean".
	Limitations []string `json:"limitations"`

	// HumanReviewItems lists everything this pass could not resolve on
	// its own: boot-disk ambiguity, unscannable partition tables, and -
	// always, today - the fact that no filesystem/OS/service/user/port
	// inventory exists yet at all.
	HumanReviewItems []string `json:"human_review_items"`
}

// DiskDiscovery is one disk's slice of a DiscoveryReport.
type DiskDiscovery struct {
	Path           string `json:"path"`
	Format         Format `json:"format"`
	SizeBytes      int64  `json:"size_bytes"`
	FormatEvidence string `json:"format_evidence"`

	// SHA256 identifies the exact source-disk content.
	SHA256 string `json:"sha256"`

	BootPartition *BootScan `json:"boot_partition,omitempty"`
	BootScanError string    `json:"boot_scan_error,omitempty"`

	// The following are always present and always reflect this
	// package's real, current capability (see unknownOperatingSystem):
	// they are never populated by this pass, on any disk, because doing
	// so honestly requires filesystem content this package deliberately
	// never reads.
	OperatingSystem      string   `json:"operating_system"`
	DetectedApplications []string `json:"detected_applications"`
	ExcludedServices     []string `json:"excluded_services"`
	PersistentData       []string `json:"persistent_data"`
	SystemDependencies   []string `json:"system_dependencies"`
	MigrationRisks       []string `json:"migration_risks"`
}

// BuildDiscoveryReport identifies every disk in paths (via
// SelectBootDisk, so the same Detect/ScanBootPartition evidence backs
// both booting and reporting) and assembles a DiscoveryReport
// describing what was found. It fails closed only on a real inspection
// failure - an unreadable path, an unrecognized format, or a digest
// read error - never merely on boot-disk ambiguity, which is instead
// recorded as a human-review item: an inconclusive report is still a
// useful, honest report, unlike a command that simply refuses to run.
func BuildDiscoveryReport(paths []string, bootDiskOverride string) (DiscoveryReport, error) {
	bootIndex, disks, selectErr := SelectBootDisk(paths, bootDiskOverride)
	if selectErr != nil && disks == nil {
		// SelectBootDisk returns a nil disks slice only when it never
		// got far enough to identify anything at all (an unreadable
		// path or unrecognized format on one of the inputs, or zero
		// paths given) - a real failure, not a reportable ambiguity.
		return DiscoveryReport{}, selectErr
	}

	report := DiscoveryReport{
		GeneratedAt: time.Now().UTC(),
		Limitations: []string{
			"disk identification is header/footer inspection only (internal/vmdisk.Detect)",
			"partition-table reading covers MBR and GPT boot-partition evidence only - no LVM, no encrypted-volume detection, no volume map",
			"no filesystem content is read (ext2/3/4, XFS, Btrfs, FAT and NTFS are all unsupported today) - operating system, services, users, ports, applications and persistent data are therefore always unknown, on every disk, regardless of what this report's per-disk fields show",
		},
	}
	if selectErr != nil {
		report.HumanReviewItems = append(report.HumanReviewItems, selectErr.Error())
	} else {
		report.BootDiskResolved = true
		report.BootDiskIndex = bootIndex
	}

	for _, disk := range disks {
		digest, err := sha256File(disk.Path)
		if err != nil {
			return DiscoveryReport{}, fmt.Errorf("vmdisk: digest %s: %w", disk.Path, err)
		}
		entry := DiskDiscovery{
			Path: disk.Path, Format: disk.Format, SizeBytes: disk.SizeBytes,
			FormatEvidence: disk.Evidence, SHA256: digest,
			BootPartition: disk.BootScan, BootScanError: disk.ScanError,
			OperatingSystem:      unknownOperatingSystem,
			DetectedApplications: []string{},
			ExcludedServices:     []string{},
			PersistentData:       []string{},
			SystemDependencies:   []string{},
			MigrationRisks:       []string{},
		}
		if disk.ScanError != "" {
			report.HumanReviewItems = append(report.HumanReviewItems,
				fmt.Sprintf("%s: partition table could not be scanned (%s)", disk.Path, disk.ScanError))
		}
		report.Disks = append(report.Disks, entry)
	}
	report.HumanReviewItems = append(report.HumanReviewItems,
		"no filesystem, operating-system, service, user, port or application inventory exists yet for any disk - manual inspection is required until that capability lands")
	return report, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// RenderText produces the human-readable report.
func (r DiscoveryReport) RenderText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Legacy VM disk discovery report - generated %s\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "%d disk(s) inspected\n\n", len(r.Disks))
	for i, disk := range r.Disks {
		marker := "  "
		if r.BootDiskResolved && i == r.BootDiskIndex {
			marker = "->"
		}
		fmt.Fprintf(&b, "%s [%d] %s\n", marker, i, disk.Path)
		fmt.Fprintf(&b, "      format:    %s (%s)\n", disk.Format, disk.FormatEvidence)
		fmt.Fprintf(&b, "      size:      %d bytes\n", disk.SizeBytes)
		fmt.Fprintf(&b, "      digest:    %s\n", disk.SHA256)
		switch {
		case disk.BootPartition != nil:
			fmt.Fprintf(&b, "      bootable:  %v (%s)\n", disk.BootPartition.Bootable, disk.BootPartition.Evidence)
		case disk.BootScanError != "":
			fmt.Fprintf(&b, "      bootable:  unknown (%s)\n", disk.BootScanError)
		}
		fmt.Fprintf(&b, "      OS:        %s\n", disk.OperatingSystem)
		b.WriteString("\n")
	}
	if !r.BootDiskResolved {
		b.WriteString("Boot disk: could not be determined automatically.\n\n")
	}
	if len(r.Limitations) > 0 {
		b.WriteString("Limitations of this analysis:\n")
		for _, limitation := range r.Limitations {
			fmt.Fprintf(&b, "  - %s\n", limitation)
		}
		b.WriteString("\n")
	}
	if len(r.HumanReviewItems) > 0 {
		b.WriteString("Requires human review:\n")
		for _, item := range r.HumanReviewItems {
			fmt.Fprintf(&b, "  - %s\n", item)
		}
	}
	return b.String()
}

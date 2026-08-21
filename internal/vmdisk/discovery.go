package vmdisk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// unknownOperatingSystem distinguishes unsupported inspection from no OS found.
const unknownOperatingSystem = "unknown: no supported filesystem provided sufficient os-release and ELF evidence"

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

	// Decisions is a machine-readable, secret-free audit trail. It refers to
	// disks by their stable input index and deliberately never repeats source
	// paths, environment values, filesystem content, keys or credentials.
	Decisions []DiscoveryDecision `json:"decisions"`
}

type DiscoveryDecision struct {
	Code      string `json:"code"`
	DiskIndex *int   `json:"disk_index,omitempty"`
	Outcome   string `json:"outcome"`
}

// DiskDiscovery is one disk's slice of a DiscoveryReport.
type DiskDiscovery struct {
	Path           string `json:"path"`
	Format         Format `json:"format"`
	SizeBytes      int64  `json:"size_bytes"`
	FormatEvidence string `json:"format_evidence"`

	// SHA256 identifies the exact source-disk content.
	SHA256 string `json:"sha256"`

	BootPartition *BootScan              `json:"boot_partition,omitempty"`
	BootScanError string                 `json:"boot_scan_error,omitempty"`
	VolumeMap     *VolumeMap             `json:"volume_map,omitempty"`
	Filesystems   []FilesystemInventory  `json:"filesystems"`
	System        *SystemInventory       `json:"system,omitempty"`
	Applications  []ApplicationInventory `json:"application_inventories"`

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
			"partition-table reading produces a bounded MBR/GPT volume map - no LVM, encrypted-volume, swap or filesystem detection",
			"ext2/3/4, FAT12/16/32, NTFS, XFS and single-device Btrfs directory metadata is inventoried read-only with strict limits; striped/parity Btrfs chunks require human migration planning",
			"operating system, services, users, ports, applications and persistent data remain unknown until semantic inventory is implemented",
		},
	}
	if selectErr != nil {
		report.HumanReviewItems = append(report.HumanReviewItems, selectErr.Error())
		report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "boot_disk_selection", Outcome: "review_required"})
	} else {
		report.BootDiskResolved = true
		report.BootDiskIndex = bootIndex
		report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "boot_disk_selection", DiskIndex: intPointer(bootIndex), Outcome: "selected"})
	}

	for diskIndex, disk := range disks {
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
			Filesystems:          []FilesystemInventory{},
			Applications:         []ApplicationInventory{},
		}
		report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "disk_format_detection", DiskIndex: intPointer(diskIndex), Outcome: string(disk.Format)})
		if volumeMap, mapErr := ScanVolumeMap(disk.Path, disk.Format); mapErr != nil {
			report.HumanReviewItems = append(report.HumanReviewItems,
				fmt.Sprintf("%s: volume map could not be produced (%s)", disk.Path, mapErr))
			report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "volume_map", DiskIndex: intPointer(diskIndex), Outcome: "failed"})
		} else {
			entry.VolumeMap = &volumeMap
			report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "volume_map", DiskIndex: intPointer(diskIndex), Outcome: fmt.Sprintf("%s:%d", volumeMap.Table, len(volumeMap.Volumes))})
			for volumeIndex := range volumeMap.Volumes {
				volume := volumeMap.Volumes[volumeIndex]
				var inventory FilesystemInventory
				var inspectErr error
				isExt := false
				switch volume.Content {
				case "unsupported-filesystem:ext2-ext3-ext4":
					isExt = true
					inventory, inspectErr = InspectExtFilesystem(disk.Path, disk.Format, volume)
				case "unsupported-filesystem:fat":
					inventory, inspectErr = InspectFATFilesystem(disk.Path, disk.Format, volume)
				case "unsupported-filesystem:ntfs":
					inventory, inspectErr = InspectNTFSFilesystem(disk.Path, disk.Format, volume)
				case "unsupported-filesystem:xfs":
					inventory, inspectErr = InspectXFSFilesystem(disk.Path, disk.Format, volume)
				case "unsupported-filesystem:btrfs":
					inventory, inspectErr = InspectBtrfsFilesystem(disk.Path, disk.Format, volume)
				default:
					continue
				}
				if inspectErr != nil {
					report.HumanReviewItems = append(report.HumanReviewItems, fmt.Sprintf("%s: volume %d filesystem inventory failed (%s)", disk.Path, volume.Index, inspectErr))
					report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "filesystem_inventory", DiskIndex: intPointer(diskIndex), Outcome: "failed"})
					continue
				}
				volumeMap.Volumes[volumeIndex].Content = inventory.Filesystem
				entry.Filesystems = append(entry.Filesystems, inventory)
				applications := DetectApplications(inventory)
				if isExt {
					applications, inspectErr = AnalyzeExtApplicationContent(disk.Path, disk.Format, volume, inventory, applications)
					if inspectErr != nil {
						report.HumanReviewItems = append(report.HumanReviewItems, fmt.Sprintf("%s: volume %d application content analysis incomplete (%s)", disk.Path, volume.Index, inspectErr))
						report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "application_content_analysis", DiskIndex: intPointer(diskIndex), Outcome: "failed"})
					} else {
						report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "application_content_analysis", DiskIndex: intPointer(diskIndex), Outcome: "completed"})
					}
				}
				entry.Applications = append(entry.Applications, applications)
				for _, executable := range applications.Executables {
					entry.DetectedApplications = append(entry.DetectedApplications, executable.Path)
				}
				dependencies := append(append(append([]ApplicationFinding{}, applications.SharedLibraries...), applications.KernelModules...), applications.ELFDependencies...)
				for _, dependency := range dependencies {
					entry.SystemDependencies = append(entry.SystemDependencies, dependency.Path)
				}
				for _, dependency := range applications.SpecialPaths {
					entry.MigrationRisks = append(entry.MigrationRisks, "special filesystem dependency requires review: "+dependency.Path)
				}
				for _, secret := range applications.SecretCandidates {
					entry.MigrationRisks = append(entry.MigrationRisks, "probable secret requires review: "+secret.Path)
				}
				report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "filesystem_inventory", DiskIndex: intPointer(diskIndex), Outcome: fmt.Sprintf("%s:%d", inventory.Filesystem, len(inventory.Files))})
				if !isExt {
					continue
				}
				system, systemErr := DetectLinuxSystem(disk.Path, disk.Format, volume, inventory)
				if systemErr != nil {
					report.HumanReviewItems = append(report.HumanReviewItems, fmt.Sprintf("%s: volume %d system detection failed (%s)", disk.Path, volume.Index, systemErr))
					report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "system_detection", DiskIndex: intPointer(diskIndex), Outcome: "failed"})
				} else if len(system.Facts) > 0 && entry.System == nil {
					entry.System = &system
					entry.OperatingSystem = strings.TrimSpace(system.Distribution + " " + system.Version)
					for _, mount := range system.Mounts {
						entry.PersistentData = append(entry.PersistentData, mount.MountPoint)
					}
					mainEvidence := ""
					if applications.MainProcess != nil {
						mainEvidence = applications.MainProcess.Evidence
					}
					for _, lifecycle := range append(append([]string{}, system.Services...), system.StartupFiles...) {
						if !strings.Contains(mainEvidence, lifecycle) {
							entry.ExcludedServices = append(entry.ExcludedServices, lifecycle)
						}
					}
					sort.Strings(entry.ExcludedServices)
					report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "system_detection", DiskIndex: intPointer(diskIndex), Outcome: "detected"})
				}
			}
		}
		switch {
		case disk.ScanError != "":
			report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "boot_partition_scan", DiskIndex: intPointer(diskIndex), Outcome: "failed"})
		case disk.BootScan != nil && disk.BootScan.Bootable:
			report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "boot_partition_scan", DiskIndex: intPointer(diskIndex), Outcome: "bootable"})
		default:
			report.Decisions = append(report.Decisions, DiscoveryDecision{Code: "boot_partition_scan", DiskIndex: intPointer(diskIndex), Outcome: "not_bootable"})
		}
		if disk.ScanError != "" {
			report.HumanReviewItems = append(report.HumanReviewItems,
				fmt.Sprintf("%s: partition table could not be scanned (%s)", disk.Path, disk.ScanError))
		}
		report.Disks = append(report.Disks, entry)
	}
	hasFilesystemInventory := false
	for _, disk := range report.Disks {
		if len(disk.Filesystems) > 0 {
			hasFilesystemInventory = true
			break
		}
	}
	if !hasFilesystemInventory {
		report.HumanReviewItems = append(report.HumanReviewItems,
			"no filesystem inventory was produced for a supported filesystem - manual inspection is required")
	}
	return report, nil
}

func intPointer(value int) *int { return &value }

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
		if disk.VolumeMap != nil {
			fmt.Fprintf(&b, "      volumes:   %s (%d)\n", disk.VolumeMap.Table, len(disk.VolumeMap.Volumes))
		}
		for _, filesystem := range disk.Filesystems {
			fmt.Fprintf(&b, "      filesystem: %s volume %d, %d entries (%d logical bytes read)\n", filesystem.Filesystem, filesystem.VolumeIndex, len(filesystem.Files), filesystem.LogicalBytesRead)
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

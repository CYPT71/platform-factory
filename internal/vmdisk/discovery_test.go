package vmdisk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestBuildDiscoveryReportSingleBootableDisk(t *testing.T) {
	osDisk := writeRawOSDisk(t, "os.raw")
	report, err := BuildDiscoveryReport([]string{osDisk}, "")
	if err != nil {
		t.Fatalf("BuildDiscoveryReport: %v", err)
	}
	if !report.BootDiskResolved || report.BootDiskIndex != 0 {
		t.Fatalf("BootDiskResolved=%v BootDiskIndex=%d", report.BootDiskResolved, report.BootDiskIndex)
	}
	if len(report.Disks) != 1 {
		t.Fatalf("Disks=%+v", report.Disks)
	}
	disk := report.Disks[0]
	if disk.Format != FormatRAW || disk.Path != osDisk {
		t.Fatalf("disk=%+v", disk)
	}
	if disk.BootPartition == nil || !disk.BootPartition.Bootable {
		t.Fatalf("BootPartition=%+v, want bootable", disk.BootPartition)
	}
	// The digest must be the exact sha256 of the file's real content -
	// this is the report's link back to the source disk.
	raw, err := os.ReadFile(osDisk)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if disk.SHA256 != want {
		t.Fatalf("SHA256=%q, want %q", disk.SHA256, want)
	}
	// Every not-yet-implemented inventory dimension must be explicitly
	// present (never nil/omitted) and honestly empty/unknown - never a
	// silent claim of "nothing found".
	if disk.OperatingSystem == "" || !strings.Contains(disk.OperatingSystem, "unknown") {
		t.Fatalf("OperatingSystem=%q, want an explicit unknown marker", disk.OperatingSystem)
	}
	if disk.DetectedApplications == nil || len(disk.DetectedApplications) != 0 {
		t.Fatalf("DetectedApplications=%+v, want non-nil empty slice", disk.DetectedApplications)
	}
	if disk.PersistentData == nil || disk.SystemDependencies == nil || disk.ExcludedServices == nil || disk.MigrationRisks == nil {
		t.Fatalf("inventory fields must never be nil: %+v", disk)
	}
	if len(report.Limitations) == 0 {
		t.Fatal("expected non-empty Limitations")
	}
	foundInventoryCaveat := false
	for _, item := range report.HumanReviewItems {
		if strings.Contains(item, "no filesystem") {
			foundInventoryCaveat = true
		}
	}
	if !foundInventoryCaveat {
		t.Fatalf("HumanReviewItems=%+v, want the standing no-inventory caveat", report.HumanReviewItems)
	}
}

func TestBuildDiscoveryReportRecordsAmbiguityAsHumanReview(t *testing.T) {
	first := writeRawOSDisk(t, "first.raw")
	second := writeRawOSDisk(t, "second.raw")
	report, err := BuildDiscoveryReport([]string{first, second}, "")
	if err != nil {
		t.Fatalf("BuildDiscoveryReport must not fail merely on boot-disk ambiguity: %v", err)
	}
	if report.BootDiskResolved {
		t.Fatal("BootDiskResolved=true, want false for an ambiguous pair")
	}
	if len(report.Disks) != 2 {
		t.Fatalf("Disks=%+v", report.Disks)
	}
	foundAmbiguity := false
	for _, item := range report.HumanReviewItems {
		if strings.Contains(item, "--boot-disk") {
			foundAmbiguity = true
		}
	}
	if !foundAmbiguity {
		t.Fatalf("HumanReviewItems=%+v, want the ambiguity reason", report.HumanReviewItems)
	}
}

func TestBuildDiscoveryReportHonorsExplicitOverride(t *testing.T) {
	first := writeRawDataDisk(t, "first.raw")
	second := writeRawDataDisk(t, "second.raw")
	report, err := BuildDiscoveryReport([]string{first, second}, second)
	if err != nil {
		t.Fatalf("BuildDiscoveryReport: %v", err)
	}
	if !report.BootDiskResolved || report.BootDiskIndex != 1 {
		t.Fatalf("BootDiskResolved=%v BootDiskIndex=%d, want true/1", report.BootDiskResolved, report.BootDiskIndex)
	}
}

func TestBuildDiscoveryReportFailsClosedOnUnreadableDisk(t *testing.T) {
	if _, err := BuildDiscoveryReport([]string{"/nonexistent/definitely-not-a-disk"}, ""); err == nil {
		t.Fatal("expected an error for an unreadable path")
	}
}

func TestBuildDiscoveryReportRequiresAtLeastOneDisk(t *testing.T) {
	if _, err := BuildDiscoveryReport(nil, ""); err == nil {
		t.Fatal("expected an error for zero disks")
	}
}

func TestDiscoveryReportRenderTextIncludesKeyFields(t *testing.T) {
	osDisk := writeRawOSDisk(t, "os.raw")
	report, err := BuildDiscoveryReport([]string{osDisk}, "")
	if err != nil {
		t.Fatal(err)
	}
	text := report.RenderText()
	for _, want := range []string{osDisk, string(FormatRAW), report.Disks[0].SHA256, "Limitations"} {
		if !strings.Contains(text, want) {
			t.Fatalf("RenderText output missing %q:\n%s", want, text)
		}
	}
}

func TestDiscoveryDecisionJournalIsStructuredAndSecretFree(t *testing.T) {
	disk := writeRawOSDisk(t, "token-super-secret.raw")
	report, err := BuildDiscoveryReport([]string{disk}, "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report.Decisions)
	if err != nil {
		t.Fatal(err)
	}
	journal := string(encoded)
	for _, forbidden := range []string{disk, "token-super-secret", "credential", "password"} {
		if strings.Contains(journal, forbidden) {
			t.Fatalf("decision journal leaked %q: %s", forbidden, journal)
		}
	}
	for _, code := range []string{"boot_disk_selection", "disk_format_detection", "volume_map", "boot_partition_scan"} {
		if !strings.Contains(journal, code) {
			t.Fatalf("decision journal missing %q: %s", code, journal)
		}
	}
}

func TestBuildDiscoveryReportInventoriesExtFilesystem(t *testing.T) {
	disk, _ := buildExtFixture(t, "ext4", false)
	report, err := BuildDiscoveryReport([]string{disk}, disk)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Disks) != 1 || len(report.Disks[0].Filesystems) != 1 {
		t.Fatalf("missing filesystem inventory: %#v", report)
	}
	filesystem := report.Disks[0].Filesystems[0]
	if filesystem.Filesystem != "ext4" || len(filesystem.Files) != 1 || report.Disks[0].VolumeMap.Volumes[0].Content != "ext4" {
		t.Fatalf("unexpected filesystem inventory: %#v", report.Disks[0])
	}
	encoded, err := json.Marshal(report.Decisions)
	if err != nil || !strings.Contains(string(encoded), "filesystem_inventory") {
		t.Fatalf("missing filesystem decision: %s err=%v", encoded, err)
	}
}

func TestBuildDiscoveryReportInventoriesFATFilesystem(t *testing.T) {
	disk, _ := buildFATFixture(t, 16, false)
	report, err := BuildDiscoveryReport([]string{disk}, disk)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Disks) != 1 || len(report.Disks[0].Filesystems) != 1 || report.Disks[0].Filesystems[0].Filesystem != "fat16" || report.Disks[0].VolumeMap.Volumes[0].Content != "fat16" {
		t.Fatalf("missing FAT inventory: %#v", report.Disks)
	}
}

func TestBuildDiscoveryReportInventoriesNTFSFilesystem(t *testing.T) {
	disk, _ := buildNTFSFixture(t, false)
	report, err := BuildDiscoveryReport([]string{disk}, disk)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Disks) != 1 || len(report.Disks[0].Filesystems) != 1 || report.Disks[0].Filesystems[0].Filesystem != "ntfs" || report.Disks[0].VolumeMap.Volumes[0].Content != "ntfs" {
		t.Fatalf("missing NTFS inventory: %#v", report.Disks)
	}
}

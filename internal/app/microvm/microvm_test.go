package microvm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/vmdisk"
)

func TestCapability(t *testing.T) {
	cases := []struct {
		action          string
		capability      string
		mutating        bool
		wantErrRejected bool
	}{
		{"create", "runtime.create", true, false},
		{"start", "runtime.start", true, false},
		{"stop", "runtime.stop", true, false},
		{"restart", "runtime.restart", true, false},
		{"delete", "runtime.delete", true, false},
		{"status", "runtime.status", false, false},
		{"logs", "runtime.logs", false, false},
		{"rbac", "runtime.rbac", true, false},
		{"bogus", "", false, true},
	}
	for _, c := range cases {
		capability, mutating, err := Capability(c.action)
		if c.wantErrRejected {
			if err == nil {
				t.Errorf("Capability(%q): expected an error", c.action)
			}
			continue
		}
		if err != nil {
			t.Errorf("Capability(%q): unexpected error %v", c.action, err)
			continue
		}
		if capability != c.capability || mutating != c.mutating {
			t.Errorf("Capability(%q) = (%q, %v), want (%q, %v)", c.action, capability, mutating, c.capability, c.mutating)
		}
	}
}

// writeRawBootDisk writes a minimal RAW disk image, carrying a valid
// MBR with one active/bootable partition entry - the same fixture
// shape internal/vmdisk's own tests use for a disk that resolves as
// the boot disk without ambiguity.
func writeRawBootDisk(t *testing.T, dir, name string) string {
	t.Helper()
	buf := make([]byte, 4096)
	buf[446] = 0x80 // active/bootable
	buf[446+4] = 0x83
	buf[510], buf[511] = 0x55, 0xaa
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// writeRawDataDisk writes a RAW disk with a partition table but no
// active/bootable flag - a non-ambiguous, non-bootable secondary disk.
func writeRawDataDisk(t *testing.T, dir, name string) string {
	t.Helper()
	buf := make([]byte, 4096)
	buf[446+4] = 0x83
	buf[510], buf[511] = 0x55, 0xaa
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestInspectLegacyDiskSuccessWritesAllFourReports covers the full
// happy path: one unambiguous bootable disk, an explicit
// vm-encapsulation strategy that is actually eligible (since a boot
// disk was resolved), and verifies both the returned Result and that
// all four report files were actually written to reportDir with
// matching content.
func TestInspectLegacyDiskSuccessWritesAllFourReports(t *testing.T) {
	srcDir := t.TempDir()
	disk := writeRawBootDisk(t, srcDir, "os.raw")
	reportDir := filepath.Join(t.TempDir(), "reports")

	result, err := InspectLegacyDisk([]string{disk}, "", reportDir, vmdisk.ModeVMEncapsulation)
	if err != nil {
		t.Fatalf("InspectLegacyDisk: %v", err)
	}

	if !result.Report.BootDiskResolved || result.Report.BootDiskIndex != 0 {
		t.Fatalf("Report.BootDiskResolved=%v Report.BootDiskIndex=%d, want true/0", result.Report.BootDiskResolved, result.Report.BootDiskIndex)
	}
	if result.Compatibility.RecommendedMode != vmdisk.ModeVMEncapsulation {
		t.Fatalf("RecommendedMode=%q, want %q", result.Compatibility.RecommendedMode, vmdisk.ModeVMEncapsulation)
	}
	if result.Compatibility.DeploymentBlocked {
		t.Fatal("DeploymentBlocked=true, want false for an eligible, explicitly-chosen encapsulation")
	}
	if !strings.Contains(result.Text, disk) {
		t.Fatalf("Text missing disk path %q:\n%s", disk, result.Text)
	}
	if !strings.Contains(result.CompatibilityText, string(vmdisk.ModeVMEncapsulation)) {
		t.Fatalf("CompatibilityText missing recommended mode:\n%s", result.CompatibilityText)
	}

	wantJSONPath := filepath.Join(reportDir, "discovery.json")
	wantTextPath := filepath.Join(reportDir, "discovery.txt")
	wantCompatJSONPath := filepath.Join(reportDir, "compatibility.json")
	wantCompatTextPath := filepath.Join(reportDir, "compatibility.txt")
	if result.JSONPath != wantJSONPath || result.TextPath != wantTextPath ||
		result.CompatibilityJSONPath != wantCompatJSONPath || result.CompatibilityTextPath != wantCompatTextPath {
		t.Fatalf("paths = %+v, want json=%s text=%s compatJSON=%s compatText=%s",
			result, wantJSONPath, wantTextPath, wantCompatJSONPath, wantCompatTextPath)
	}

	for _, path := range []string{wantJSONPath, wantTextPath, wantCompatJSONPath, wantCompatTextPath} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("expected %s to exist: %v", path, statErr)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", path)
		}
	}

	onDiskText, err := os.ReadFile(wantTextPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantTextPath, err)
	}
	if string(onDiskText) != result.Text {
		t.Fatalf("discovery.txt on disk does not match Result.Text")
	}
	onDiskCompatText, err := os.ReadFile(wantCompatTextPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantCompatTextPath, err)
	}
	if string(onDiskCompatText) != result.CompatibilityText {
		t.Fatalf("compatibility.txt on disk does not match Result.CompatibilityText")
	}
}

// TestInspectLegacyDiskDefaultsToUnsupportedWithoutAnExplicitStrategy
// exercises the empty-strategy branch: BuildCompatibilityReport
// defaults to ModeAuto, which - since no strategy proves any mode
// safe today - always recommends ModeUnsupported and blocks
// deployment, even for an unambiguous, bootable disk.
func TestInspectLegacyDiskDefaultsToUnsupportedWithoutAnExplicitStrategy(t *testing.T) {
	srcDir := t.TempDir()
	disk := writeRawBootDisk(t, srcDir, "os.raw")
	reportDir := t.TempDir()

	result, err := InspectLegacyDisk([]string{disk}, "", reportDir, "")
	if err != nil {
		t.Fatalf("InspectLegacyDisk: %v", err)
	}
	if result.Compatibility.RequestedStrategy != vmdisk.ModeAuto {
		t.Fatalf("RequestedStrategy=%q, want %q (empty strategy defaults to auto)", result.Compatibility.RequestedStrategy, vmdisk.ModeAuto)
	}
	if result.Compatibility.RecommendedMode != vmdisk.ModeUnsupported {
		t.Fatalf("RecommendedMode=%q, want %q", result.Compatibility.RecommendedMode, vmdisk.ModeUnsupported)
	}
	if !result.Compatibility.DeploymentBlocked {
		t.Fatal("DeploymentBlocked=false, want true when no strategy can be proven safe")
	}
}

// TestInspectLegacyDiskHandlesAmbiguousBootDiskAsAReportableOutcome
// covers BuildDiscoveryReport's own "ambiguity is not an error" rule
// flowing all the way through InspectLegacyDisk: two disks with no
// boot evidence resolves as a valid (if incomplete) report rather
// than failing, and the compatibility report correctly marks
// vm-encapsulation as ineligible.
func TestInspectLegacyDiskHandlesAmbiguousBootDiskAsAReportableOutcome(t *testing.T) {
	srcDir := t.TempDir()
	first := writeRawDataDisk(t, srcDir, "first.raw")
	second := writeRawDataDisk(t, srcDir, "second.raw")
	reportDir := t.TempDir()

	result, err := InspectLegacyDisk([]string{first, second}, "", reportDir, vmdisk.ModeVMEncapsulation)
	if err != nil {
		t.Fatalf("InspectLegacyDisk must not fail merely on boot-disk ambiguity: %v", err)
	}
	if result.Report.BootDiskResolved {
		t.Fatal("BootDiskResolved=true, want false for an ambiguous pair")
	}
	if result.Compatibility.RecommendedMode == vmdisk.ModeVMEncapsulation {
		t.Fatal("encapsulation must not be recommended when no boot disk was resolved")
	}
	found := false
	for _, item := range result.Report.HumanReviewItems {
		if strings.Contains(item, "--boot-disk") {
			found = true
		}
	}
	if !found {
		t.Fatalf("HumanReviewItems=%+v, want the ambiguity reason surfaced through to the Result", result.Report.HumanReviewItems)
	}
}

// TestInspectLegacyDiskFailsClosedOnDiscoveryError covers the first
// error return: a discovery failure (here, zero disk images) must
// surface as a plain error, never wrapped in ErrCompatibilityReport -
// that sentinel is reserved for BuildCompatibilityReport failures
// specifically, per the package's own doc comment.
func TestInspectLegacyDiskFailsClosedOnDiscoveryError(t *testing.T) {
	reportDir := t.TempDir()
	_, err := InspectLegacyDisk(nil, "", reportDir, vmdisk.ModeAuto)
	if err == nil {
		t.Fatal("expected an error for zero disk images")
	}
	if errors.Is(err, ErrCompatibilityReport) {
		t.Fatalf("err = %v, must not be ErrCompatibilityReport for a discovery-stage failure", err)
	}
}

// TestInspectLegacyDiskWrapsCompatibilityReportError covers the
// ErrCompatibilityReport wrapping branch: an unrecognized --strategy
// reaches BuildCompatibilityReport (discovery itself succeeds) and
// must come back wrapped in the documented sentinel so callers can
// distinguish it from an operational failure.
func TestInspectLegacyDiskWrapsCompatibilityReportError(t *testing.T) {
	srcDir := t.TempDir()
	disk := writeRawBootDisk(t, srcDir, "os.raw")
	reportDir := t.TempDir()

	_, err := InspectLegacyDisk([]string{disk}, "", reportDir, vmdisk.ExecutionMode("not-a-real-strategy"))
	if err == nil {
		t.Fatal("expected an error for an unrecognized strategy")
	}
	if !errors.Is(err, ErrCompatibilityReport) {
		t.Fatalf("err = %v, want it to wrap ErrCompatibilityReport", err)
	}
}

// TestInspectLegacyDiskFailsWhenReportDirCannotBeCreated covers the
// os.MkdirAll(reportDir, ...) error branch: reportDir's parent segment
// is an ordinary file, so MkdirAll can never turn it into a directory.
func TestInspectLegacyDiskFailsWhenReportDirCannotBeCreated(t *testing.T) {
	srcDir := t.TempDir()
	disk := writeRawBootDisk(t, srcDir, "os.raw")

	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(blocker, "reports")

	_, err := InspectLegacyDisk([]string{disk}, "", reportDir, vmdisk.ModeAuto)
	if err == nil {
		t.Fatal("expected an error when reportDir's parent is a regular file")
	}
	if errors.Is(err, ErrCompatibilityReport) {
		t.Fatalf("err = %v, must not be ErrCompatibilityReport for a report-dir failure", err)
	}
}

// TestInspectLegacyDiskFailsWhenReportDirNotWritable covers the
// atomicfile.Write failure branch for the very first report file
// (discovery.json): reportDir already exists (so MkdirAll is a no-op)
// but has no write permission, so the atomic write's temp-file
// creation must fail and InspectLegacyDisk must surface that as an
// error naming discovery.json.
func TestInspectLegacyDiskFailsWhenReportDirNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	srcDir := t.TempDir()
	disk := writeRawBootDisk(t, srcDir, "os.raw")

	reportDir := t.TempDir()
	if err := os.Chmod(reportDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(reportDir, 0o755)

	_, err := InspectLegacyDisk([]string{disk}, "", reportDir, vmdisk.ModeAuto)
	if err == nil {
		t.Fatal("expected an error when reportDir is not writable")
	}
	if !strings.Contains(err.Error(), "discovery.json") {
		t.Fatalf("err = %v, want it to name discovery.json as the file that failed to write", err)
	}
}

// TestInspectLegacyDiskHonorsBootDiskOverride covers the
// bootDiskOverride parameter flowing through to
// vmdisk.BuildDiscoveryReport/SelectBootDisk: with two otherwise-
// ambiguous disks, an explicit override still resolves a boot disk
// and allows vm-encapsulation to be recommended.
func TestInspectLegacyDiskHonorsBootDiskOverride(t *testing.T) {
	srcDir := t.TempDir()
	first := writeRawDataDisk(t, srcDir, "first.raw")
	second := writeRawDataDisk(t, srcDir, "second.raw")
	reportDir := t.TempDir()

	result, err := InspectLegacyDisk([]string{first, second}, second, reportDir, vmdisk.ModeVMEncapsulation)
	if err != nil {
		t.Fatalf("InspectLegacyDisk: %v", err)
	}
	if !result.Report.BootDiskResolved || result.Report.BootDiskIndex != 1 {
		t.Fatalf("BootDiskResolved=%v BootDiskIndex=%d, want true/1 (override selects second disk)", result.Report.BootDiskResolved, result.Report.BootDiskIndex)
	}
	if result.Compatibility.RecommendedMode != vmdisk.ModeVMEncapsulation {
		t.Fatalf("RecommendedMode=%q, want %q once the override resolves a boot disk", result.Compatibility.RecommendedMode, vmdisk.ModeVMEncapsulation)
	}
}

// TestInspectLegacyDiskFailsWhenDiscoveryTextPathIsBlocked covers the
// atomicfile.Write failure branch for discovery.txt specifically (as
// opposed to discovery.json, already covered by
// TestInspectLegacyDiskFailsWhenReportDirNotWritable): reportDir is
// otherwise writable, but "discovery.txt" already exists as a
// directory, so the atomic write's final os.Rename onto that path
// must fail - after discovery.json has already been written
// successfully, proving the second write's own error path (and not
// just the first) is reachable and surfaced correctly.
func TestInspectLegacyDiskFailsWhenDiscoveryTextPathIsBlocked(t *testing.T) {
	srcDir := t.TempDir()
	disk := writeRawBootDisk(t, srcDir, "os.raw")

	reportDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(reportDir, "discovery.txt"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := InspectLegacyDisk([]string{disk}, "", reportDir, vmdisk.ModeAuto)
	if err == nil {
		t.Fatal("expected an error when discovery.txt cannot be written")
	}
	if !strings.Contains(err.Error(), "discovery.txt") {
		t.Fatalf("err = %v, want it to name discovery.txt as the file that failed to write", err)
	}
	if _, statErr := os.Stat(filepath.Join(reportDir, "discovery.json")); statErr != nil {
		t.Fatalf("expected discovery.json to have been written before the discovery.txt failure: %v", statErr)
	}
}

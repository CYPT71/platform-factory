package vmdisk

import (
	"strings"
	"testing"
)

func TestCompatibilityAutoFailsClosedWithoutFilesystemEvidence(t *testing.T) {
	discovery, err := BuildDiscoveryReport([]string{writeRawOSDisk(t, "os.raw")}, "")
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildCompatibilityReport(discovery, ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if report.RecommendedMode != ModeUnsupported || !report.DeploymentBlocked || report.Confidence != 0 || report.AutomaticDecision {
		t.Fatalf("report=%+v", report)
	}
	if len(report.BlockingConditions) == 0 || report.DiscoveryDigest == "" {
		t.Fatalf("report=%+v", report)
	}
}

func TestCompatibilityAllowsOnlyExplicitResolvedEncapsulation(t *testing.T) {
	discovery, err := BuildDiscoveryReport([]string{writeRawOSDisk(t, "os.raw")}, "")
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildCompatibilityReport(discovery, ModeVMEncapsulation)
	if err != nil {
		t.Fatal(err)
	}
	if report.RecommendedMode != ModeVMEncapsulation || report.DeploymentBlocked || report.AutomaticDecision || report.Confidence <= 0 {
		t.Fatalf("report=%+v", report)
	}
	if !strings.Contains(report.RenderText(), "Deployment blocked: false") {
		t.Fatal(report.RenderText())
	}
}

func TestCompatibilityRefusesUnprovenExplicitExtraction(t *testing.T) {
	discovery, err := BuildDiscoveryReport([]string{writeRawOSDisk(t, "os.raw")}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []ExecutionMode{ModeContainer, ModeMicroVMOCI, ModeMicroVMDirect} {
		report, err := BuildCompatibilityReport(discovery, strategy)
		if err != nil {
			t.Fatal(err)
		}
		if report.RecommendedMode != ModeUnsupported || !report.DeploymentBlocked {
			t.Fatalf("strategy=%s report=%+v", strategy, report)
		}
	}
}

func TestCompatibilityRejectsUnknownStrategyAndAmbiguousEncapsulation(t *testing.T) {
	discovery, err := BuildDiscoveryReport([]string{writeRawOSDisk(t, "a.raw"), writeRawOSDisk(t, "b.raw")}, "")
	if err != nil {
		t.Fatal(err)
	}
	report, err := BuildCompatibilityReport(discovery, ModeVMEncapsulation)
	if err != nil {
		t.Fatal(err)
	}
	if !report.DeploymentBlocked || report.RecommendedMode != ModeUnsupported {
		t.Fatalf("report=%+v", report)
	}
	if _, err := BuildCompatibilityReport(discovery, "magic"); err == nil {
		t.Fatal("expected invalid strategy")
	}
}

func TestCompatibilityClassifiesCompleteContainerEvidence(t *testing.T) {
	main := finding("/usr/bin/demo", "main_process", "high", "ExecStart in demo.service")
	discovery := DiscoveryReport{Disks: []DiskDiscovery{{
		Filesystems:    []FilesystemInventory{{Filesystem: "ext4"}},
		System:         &SystemInventory{Services: []string{"/etc/systemd/system/demo.service"}},
		PersistentData: []string{"/srv/data"},
		Applications: []ApplicationInventory{{
			MainProcess:     &main,
			SharedLibraries: []ApplicationFinding{finding("/usr/lib/libc.so", "shared_library", "high", "fixture")},
			ELFDependencies: []ApplicationFinding{finding("libc.so", "elf_dependency", "high", "fixture")},
		}},
	}}}
	report, err := BuildCompatibilityReport(discovery, ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if report.RecommendedMode != ModeContainer || report.DeploymentBlocked || !report.AutomaticDecision || report.Classification.SafeOCIExtraction.Decision != "yes" || report.Classification.DataSeparable.Decision != "yes" {
		t.Fatalf("report=%+v", report)
	}
}

func TestCompatibilityRequiresMicroVMForKernelCoupling(t *testing.T) {
	main := finding("/usr/bin/demo", "main_process", "high", "fixture")
	discovery := DiscoveryReport{Disks: []DiskDiscovery{{
		Filesystems: []FilesystemInventory{{Filesystem: "ext4"}}, System: &SystemInventory{},
		Applications: []ApplicationInventory{{MainProcess: &main, SpecialPaths: []ApplicationFinding{finding("/dev", "special_filesystem_dependency", "medium", "fixture")}}},
	}}}
	report, err := BuildCompatibilityReport(discovery, ModeAuto)
	if err != nil {
		t.Fatal(err)
	}
	if report.RecommendedMode != ModeUnsupported || !report.DeploymentBlocked || report.Classification.MicroVMRequired.Decision != "yes" || report.Classification.DeviceRequired.Decision != "no" {
		t.Fatalf("report=%+v", report)
	}
}

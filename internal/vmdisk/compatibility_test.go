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

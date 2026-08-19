package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/budget"
	"github.com/CYPT71/platform-factory/internal/policy"
	"github.com/CYPT71/platform-factory/internal/sbom"
	"github.com/CYPT71/platform-factory/oci"
)

// writeExecutable writes an "unknown, non-ambiguous" input (arbitrary
// bytes that are neither an ELF header nor a shebang line) so it always
// passes ResolveTarget's detected.Kind check without needing a real ELF
// binary, matching how oci.Build itself is exercised elsewhere with a
// plain executable payload file.
func writeExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseByteLimit(t *testing.T) {
	for _, args := range []string{"12MB", "-1"} {
		if _, err := ParseByteLimit(args); err == nil {
			t.Fatalf("expected %q to be rejected", args)
		}
	}
	for input, want := range map[string]int64{"0": 0, "512MiB": 512 << 20, "2GiB": 2 << 30, "4096": 4096} {
		got, err := ParseByteLimit(input)
		if err != nil || got != want {
			t.Fatalf("ParseByteLimit(%q)=%d,%v want=%d", input, got, err, want)
		}
	}
}

func TestTargetsRejectsAmbiguousSyntax(t *testing.T) {
	for _, test := range []struct {
		platforms, positional []string
	}{
		{nil, nil},
		{[]string{"bad"}, []string{"app"}},
		{[]string{"linux/amd64=app"}, nil},
		{[]string{"linux/amd64=app", "linux/arm64"}, nil},
		{[]string{"linux/amd64=app", "linux/arm64=other"}, []string{"extra"}},
	} {
		if _, _, err := Targets(test.platforms, test.positional, "linux", "amd64"); err == nil {
			t.Fatalf("accepted platforms=%v positional=%v", test.platforms, test.positional)
		}
	}
}

// TestTargetsAndResolveTargetRemainingErrorBranches closes the few
// Targets/ResolveTarget error paths not already exercised through
// TestTargetsRejectsAmbiguousSyntax: a single --platform without exactly
// one executable, an invalid platform inside multi-platform syntax, and
// a non-executable input (detected as a different, unambiguous kind)
// reaching ResolveTarget.
func TestTargetsAndResolveTargetRemainingErrorBranches(t *testing.T) {
	if _, code, err := Targets([]string{"linux/amd64"}, nil, "linux", "amd64"); err == nil || code != 2 {
		t.Fatalf("single platform without executable: code=%d err=%v", code, err)
	}
	if _, code, err := Targets(
		[]string{"bogus/arch=a", "linux/amd64=b"}, nil, "linux", "amd64",
	); err == nil || code != 2 {
		t.Fatalf("invalid platform in multi-platform syntax: code=%d err=%v", code, err)
	}
	root := t.TempDir()
	script := filepath.Join(root, "app.py")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveTarget(Target{OS: "linux", Architecture: "amd64", Input: script}, Settings{}); err == nil {
		t.Fatal("expected a non-ELF, non-unknown input to be rejected")
	}
}

func TestResourceBudgetPlanRendersHumanReadableLimits(t *testing.T) {
	plan := ResourceBudgetPlan(budget.Budget{WallClock: 90 * time.Second, CPU: 2 * time.Minute, Memory: 512 << 20})
	if plan["max_wall_clock"] != "1m30s" || plan["max_cpu"] != "2m0s" || plan["max_memory_bytes"] != int64(512<<20) {
		t.Fatalf("plan=%v", plan)
	}
}

// TestResolveTargetAppliesOverridesInPriorityOrder exercises
// ResolveTarget's success path: the detected default, then the config
// file override, then the explicit settings override winning in that
// order for both entrypoint and profile independently.
func TestResolveTargetAppliesOverridesInPriorityOrder(t *testing.T) {
	root := t.TempDir()
	input := writeExecutable(t, root, "service", "not an elf and no shebang here")
	target := Target{OS: "linux", Architecture: "amd64", Input: input}

	entrypoint, profile, err := ResolveTarget(target, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if entrypoint != "/app/service" || profile != "static" {
		t.Fatalf("defaults: entrypoint=%q profile=%q", entrypoint, profile)
	}

	entrypoint, profile, err = ResolveTarget(target, Settings{
		Config: oci.BuildConfig{Entrypoint: "/srv/config-entry", Profile: "config-profile"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if entrypoint != "/srv/config-entry" || profile != "config-profile" {
		t.Fatalf("config override: entrypoint=%q profile=%q", entrypoint, profile)
	}

	entrypoint, profile, err = ResolveTarget(target, Settings{
		Config:     oci.BuildConfig{Entrypoint: "/srv/config-entry", Profile: "config-profile"},
		Entrypoint: "/srv/flag-entry", Profile: "flag-profile",
	})
	if err != nil {
		t.Fatal(err)
	}
	if entrypoint != "/srv/flag-entry" || profile != "flag-profile" {
		t.Fatalf("explicit override: entrypoint=%q profile=%q", entrypoint, profile)
	}
}

func TestResolveTargetPropagatesDetectStatError(t *testing.T) {
	target := Target{OS: "linux", Architecture: "amd64", Input: filepath.Join(t.TempDir(), "missing")}
	if _, _, err := ResolveTarget(target, Settings{}); err == nil {
		t.Fatal("expected an error for a nonexistent input")
	}
}

func TestBuildImageSucceedsAndReportsDigestPlatformProfile(t *testing.T) {
	root := t.TempDir()
	input := writeExecutable(t, root, "service", "payload")
	target := Target{OS: "linux", Architecture: "arm64", Input: input}
	output := filepath.Join(root, "layout")

	result, code, err := BuildImage(target, output, Settings{})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if result["architecture"] != "arm64" || result["platform"] != "linux/arm64" || result["profile"] != "static" {
		t.Fatalf("result=%v", result)
	}
	digest, _ := result["digest"].(string)
	if digest == "" {
		t.Fatalf("result=%v missing digest", result)
	}
}

func TestBuildImageResolveTargetFailureReturnsUsageExitCode(t *testing.T) {
	target := Target{OS: "linux", Architecture: "amd64", Input: filepath.Join(t.TempDir(), "missing")}
	result, code, err := BuildImage(target, filepath.Join(t.TempDir(), "layout"), Settings{})
	if err == nil || code != 2 || result != nil {
		t.Fatalf("result=%v code=%d err=%v", result, code, err)
	}
}

func TestBuildImageOCIBuilderFailureReturnsFailureExitCode(t *testing.T) {
	root := t.TempDir()
	input := writeExecutable(t, root, "service", "payload")
	target := Target{OS: "linux", Architecture: "amd64", Input: input}
	output := filepath.Join(root, "layout")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	result, code, err := BuildImage(target, output, Settings{})
	if err == nil || code != 1 || result != nil {
		t.Fatalf("result=%v code=%d err=%v, want a failure when output already exists", result, code, err)
	}
}

func TestWriteSBOMToDistCoversEntrypointAndExtraFiles(t *testing.T) {
	root := t.TempDir()
	input := writeExecutable(t, root, "service", "payload")
	extra := writeExecutable(t, root, "helper", "helper payload")
	target := Target{OS: "linux", Architecture: "amd64", Input: input}
	settings := Settings{ExtraFiles: []oci.ExtraFile{{Dest: "/app/helper", Source: extra}}}
	distDir := filepath.Join(root, "dist")

	if err := WriteSBOMToDist(distDir, target, settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(distDir, "sbom.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document sbom.Document
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, component := range document.Components {
		names[component.Name] = true
	}
	if !names["/app/service"] || !names["/app/helper"] {
		t.Fatalf("components=%+v, want /app/service and /app/helper", document.Components)
	}
}

func TestWriteSBOMToDistPropagatesResolveTargetError(t *testing.T) {
	target := Target{OS: "linux", Architecture: "amd64", Input: filepath.Join(t.TempDir(), "missing")}
	if err := WriteSBOMToDist(t.TempDir(), target, Settings{}); err == nil {
		t.Fatal("expected an error when the target input cannot be detected")
	}
}

func TestWriteSBOMToDistPropagatesGenerateErrorForMissingExtraFile(t *testing.T) {
	root := t.TempDir()
	input := writeExecutable(t, root, "service", "payload")
	target := Target{OS: "linux", Architecture: "amd64", Input: input}
	settings := Settings{ExtraFiles: []oci.ExtraFile{{Dest: "/app/missing", Source: filepath.Join(root, "does-not-exist")}}}
	if err := WriteSBOMToDist(filepath.Join(root, "dist"), target, settings); err == nil {
		t.Fatal("expected an error when an extra file's source is missing")
	}
}

func TestWriteSBOMToDistSurfacesMkdirAllFailure(t *testing.T) {
	root := t.TempDir()
	input := writeExecutable(t, root, "service", "payload")
	target := Target{OS: "linux", Architecture: "amd64", Input: input}
	blocked := writeExecutable(t, root, "blocked-file", "x")
	distDir := filepath.Join(blocked, "dist")
	if err := WriteSBOMToDist(distDir, target, Settings{}); err == nil {
		t.Fatal("expected an error when distDir's parent is a regular file")
	}
}

func TestWriteBuildEvidenceRequiresSubjectDigest(t *testing.T) {
	err := WriteBuildEvidence("", "", "", "", "1.0.0", map[string]any{}, Target{}, Settings{})
	if err == nil {
		t.Fatal("expected an error when result has no digest")
	}
}

func TestWriteBuildEvidenceWritesUnsignedDistAndReportsArtifacts(t *testing.T) {
	root := t.TempDir()
	distDir := filepath.Join(root, "dist")
	reportsDir := filepath.Join(root, "reports")
	target := Target{OS: "linux", Architecture: "amd64"}
	settings := Settings{Entrypoint: "/app/service", Created: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	result := map[string]any{"digest": "sha256:abc"}

	if err := WriteBuildEvidence(distDir, reportsDir, "", "", "1.2.3", result, target, settings); err != nil {
		t.Fatal(err)
	}

	provenanceData, err := os.ReadFile(filepath.Join(distDir, "provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	var provenance map[string]any
	if err := json.Unmarshal(provenanceData, &provenance); err != nil {
		t.Fatal(err)
	}
	if provenance["subject_digest"] != "sha256:abc" || provenance["builder"] != "platform-factory/1.2.3" ||
		provenance["platform"] != "linux/amd64" || provenance["entrypoint"] != "/app/service" {
		t.Fatalf("provenance=%v", provenance)
	}
	if _, err := os.Stat(filepath.Join(distDir, "attestations")); !os.IsNotExist(err) {
		t.Fatalf("attestations dir should not exist without a sign key, stat err=%v", err)
	}

	for _, name := range []string{"policy-rules.json", "evidence.json", "policy.json", "summary.txt"} {
		if _, err := os.Stat(filepath.Join(reportsDir, name)); err != nil {
			t.Fatalf("expected %s to be written: %v", name, err)
		}
	}
	evidenceData, err := os.ReadFile(filepath.Join(reportsDir, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence policy.Evidence
	if err := json.Unmarshal(evidenceData, &evidence); err != nil {
		t.Fatal(err)
	}
	if !evidence.SBOM || !evidence.Provenance || evidence.Signature {
		t.Fatalf("evidence=%+v, want SBOM/Provenance true and Signature false (distDir set, no sign key)", evidence)
	}
}

func TestWriteBuildEvidenceEmptyDistDirSkipsArtifactsButKeepsPolicyReport(t *testing.T) {
	root := t.TempDir()
	reportsDir := filepath.Join(root, "reports")
	result := map[string]any{"digest": "sha256:abc"}

	if err := WriteBuildEvidence("", reportsDir, "", "", "1.0.0", result, Target{OS: "linux", Architecture: "amd64"}, Settings{}); err != nil {
		t.Fatal(err)
	}
	evidenceData, err := os.ReadFile(filepath.Join(reportsDir, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence policy.Evidence
	if err := json.Unmarshal(evidenceData, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.SBOM || evidence.Provenance {
		t.Fatalf("evidence=%+v, want SBOM/Provenance false when distDir is empty", evidence)
	}
}

func TestWriteBuildEvidenceEmptyReportsDirSkipsReports(t *testing.T) {
	result := map[string]any{"digest": "sha256:abc"}
	if err := WriteBuildEvidence("", "", "", "", "1.0.0", result, Target{OS: "linux", Architecture: "amd64"}, Settings{}); err != nil {
		t.Fatal(err)
	}
}

func TestWriteBuildEvidenceSignsAndWritesAttestations(t *testing.T) {
	root := t.TempDir()
	distDir := filepath.Join(root, "dist")
	keyDir := filepath.Join(root, "keys")
	settings := Settings{Image: "registry.example/app", Tag: "v1"}
	result := map[string]any{"digest": "sha256:def"}

	if err := WriteBuildEvidence(distDir, "", keyDir, "release", "1.0.0", result, Target{OS: "linux", Architecture: "amd64"}, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(distDir, "attestations", "provenance.dsse.json")); err != nil {
		t.Fatalf("expected a signed provenance envelope: %v", err)
	}
	if _, err := os.Stat(filepath.Join(distDir, "signatures", "subject.dsse.json")); err != nil {
		t.Fatalf("expected a signed subject envelope: %v", err)
	}
}

func TestWriteBuildEvidenceSigningKeyStoreFailure(t *testing.T) {
	root := t.TempDir()
	blocked := writeExecutable(t, root, "blocked", "x")
	result := map[string]any{"digest": "sha256:abc"}
	err := WriteBuildEvidence(filepath.Join(root, "dist"), "", blocked, "release", "1.0.0",
		result, Target{OS: "linux", Architecture: "amd64"}, Settings{})
	if err == nil {
		t.Fatal("expected an error when signKeyDir is a regular file")
	}
}

func TestWriteBuildEvidenceSigningPublicKeyFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	root := t.TempDir()
	keyDir := filepath.Join(root, "keys")
	if err := os.MkdirAll(keyDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(keyDir, 0o755)
	result := map[string]any{"digest": "sha256:abc"}
	err := WriteBuildEvidence(filepath.Join(root, "dist"), "", keyDir, "release", "1.0.0",
		result, Target{OS: "linux", Architecture: "amd64"}, Settings{})
	if err == nil {
		t.Fatal("expected an error when the signing key directory is unwritable")
	}
}

func TestWriteBuildEvidenceDistDirWriteFailure(t *testing.T) {
	root := t.TempDir()
	blocked := writeExecutable(t, root, "blocked", "x")
	result := map[string]any{"digest": "sha256:abc"}
	err := WriteBuildEvidence(filepath.Join(blocked, "dist"), "", "", "", "1.0.0",
		result, Target{OS: "linux", Architecture: "amd64"}, Settings{})
	if err == nil {
		t.Fatal("expected an error when distDir's parent is a regular file")
	}
}

func TestWriteBuildEvidenceReportsDirWriteFailure(t *testing.T) {
	root := t.TempDir()
	blocked := writeExecutable(t, root, "blocked", "x")
	result := map[string]any{"digest": "sha256:abc"}
	err := WriteBuildEvidence("", filepath.Join(blocked, "reports"), "", "", "1.0.0",
		result, Target{OS: "linux", Architecture: "amd64"}, Settings{})
	if err == nil {
		t.Fatal("expected an error when reportsDir's parent is a regular file")
	}
}

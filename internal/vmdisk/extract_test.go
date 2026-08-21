package vmdisk

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractSelectedExtFilesInstallsAtomicallyAndPreservesMetadata(t *testing.T) {
	disk, volume := buildExtFixture(t, "ext4", false)
	inventory, err := InspectExtFilesystem(disk, FormatRAW, volume)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture advertises an xattr pointer. Model a fully supported file in
	// this positive extraction test; the next test proves the real xattr case
	// needs explicit incomplete approval.
	inventory.Files[0].ExtendedAttributes = false
	output := filepath.Join(t.TempDir(), "extracted")
	report, err := ExtractSelectedExtFiles(ExtractionOptions{DiskPath: disk, Format: FormatRAW, Volume: volume, Inventory: inventory, SelectedPaths: []string{"/hello.txt"}, Output: output})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(output, "hello.txt"))
	if err != nil || string(content) != "hello" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	info, err := os.Stat(filepath.Join(output, "hello.txt"))
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
	if !report.Complete || report.TotalBytes != 5 || len(report.Extracted) != 1 || report.Extracted[0].UID != 1000 || report.Extracted[0].GID != 1001 {
		t.Fatalf("report=%+v", report)
	}
	var persisted ExtractionReport
	encoded, err := os.ReadFile(filepath.Join(output, "extraction-report.json"))
	if err != nil || json.Unmarshal(encoded, &persisted) != nil || persisted.Extracted[0].Mode != 0o640 {
		t.Fatalf("persisted report=%+v err=%v", persisted, err)
	}
}

func TestExtractSelectedExtFilesRefusesUndeclaredIncompleteExtraction(t *testing.T) {
	disk, volume := buildExtFixture(t, "ext2", false)
	inventory, err := InspectExtFilesystem(disk, FormatRAW, volume)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	output := filepath.Join(parent, "must-not-exist")
	report, err := ExtractSelectedExtFiles(ExtractionOptions{DiskPath: disk, Format: FormatRAW, Volume: volume, Inventory: inventory, SelectedPaths: []string{"/hello.txt"}, Output: output})
	if !errors.Is(err, ErrIncompleteExtraction) || report.Complete || len(report.Excluded) != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("incomplete extraction mutated output: %v", statErr)
	}
	report, err = ExtractSelectedExtFiles(ExtractionOptions{DiskPath: disk, Format: FormatRAW, Volume: volume, Inventory: inventory, SelectedPaths: []string{"/hello.txt"}, Output: output, AllowIncomplete: true})
	if err != nil || report.Complete || !report.ApprovedIncomplete {
		t.Fatalf("approved report=%+v err=%v", report, err)
	}
}

func TestExtractSelectedExtFilesRejectsTraversalBeforeMutation(t *testing.T) {
	disk, volume := buildExtFixture(t, "ext2", false)
	inventory, err := InspectExtFilesystem(disk, FormatRAW, volume)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "must-not-exist")
	for _, selected := range []string{"../hello.txt", "/../hello.txt", "/", "/hello.txt", "/hello.txt"} {
		paths := []string{selected}
		if selected == "/hello.txt" {
			paths = []string{selected, selected}
		}
		if _, err := ExtractSelectedExtFiles(ExtractionOptions{DiskPath: disk, Format: FormatRAW, Volume: volume, Inventory: inventory, SelectedPaths: paths, Output: output}); err == nil {
			t.Fatalf("selection %q was accepted", paths)
		}
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid selection mutated output: %v", err)
	}
}

func TestLegacyExtractionSecretFilenamePolicy(t *testing.T) {
	for _, value := range []string{"/srv/app/.env", "/root/id_ed25519", "/etc/cloud/credentials", "/opt/API_TOKEN.json"} {
		if !isSecretCandidate(value) {
			t.Fatalf("secret candidate %q was not classified", value)
		}
	}
	for _, value := range []string{"/srv/app/main.go", "/etc/hosts", "/opt/tokenizer/model"} {
		if isSecretCandidate(value) {
			t.Fatalf("ordinary filename %q was classified as a secret", value)
		}
	}
}

func TestLegacyExtractionOmitsSecretServiceEnvironmentByDefault(t *testing.T) {
	disk, volume := buildExtFixture(t, "ext2", false)
	inventory, err := InspectExtFilesystem(disk, FormatRAW, volume)
	if err != nil {
		t.Fatal(err)
	}
	inventory.Files[0].ExtendedAttributes = false
	system := &SystemInventory{ServiceConfigurations: []ServiceConfiguration{{Source: "/etc/systemd/system/demo.service", Entrypoint: "/hello.txt", Environment: map[string]string{"MODE": "production"}, SecretEnvironmentKeys: []string{"API_TOKEN"}}}}
	output := filepath.Join(t.TempDir(), "must-not-exist")
	report, err := ExtractSelectedExtFiles(ExtractionOptions{DiskPath: disk, Format: FormatRAW, Volume: volume, Inventory: inventory, SelectedPaths: []string{"/hello.txt"}, Output: output, System: system, SelectedEntrypoint: "/hello.txt"})
	if !errors.Is(err, ErrIncompleteExtraction) || report.Complete || len(report.Excluded) != 1 || report.Excluded[0].Reason != "secret environment value API_TOKEN omitted" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("secret-bearing service mutated output: %v", err)
	}
}

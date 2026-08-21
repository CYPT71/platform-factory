package vmdisk

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxExtractedFiles = 1024
	maxExtractedFile  = 64 << 20
	maxExtractedTotal = 256 << 20
)

var ErrIncompleteExtraction = errors.New("vmdisk: incomplete extraction requires explicit approval")

type ExtractionOptions struct {
	DiskPath           string
	Format             Format
	Volume             Volume
	Inventory          FilesystemInventory
	SelectedPaths      []string
	Output             string
	AllowIncomplete    bool
	IncludeSecrets     bool
	System             *SystemInventory
	SelectedEntrypoint string
}

type ExtractedFile struct {
	Path               string `json:"path"`
	Mode               uint16 `json:"mode"`
	UID                uint32 `json:"uid"`
	GID                uint32 `json:"gid"`
	Size               uint64 `json:"size"`
	ExtendedAttributes bool   `json:"extended_attributes"`
}

type ExtractionExclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type ExtractionReport struct {
	APIVersion         string                `json:"api_version"`
	DiskPath           string                `json:"disk_path"`
	VolumeIndex        int                   `json:"volume_index"`
	Filesystem         string                `json:"filesystem"`
	Output             string                `json:"output"`
	Complete           bool                  `json:"complete"`
	ApprovedIncomplete bool                  `json:"approved_incomplete"`
	Extracted          []ExtractedFile       `json:"extracted"`
	Excluded           []ExtractionExclusion `json:"excluded"`
	TotalBytes         uint64                `json:"total_bytes"`
	System             *SystemInventory      `json:"system,omitempty"`
}

// ExtractSelectedExtFiles copies an explicit allowlist of regular files from
// one inventoried ext volume into a new directory. It never follows disk or
// filesystem symlinks, never overwrites an output, and installs the directory
// with one rename only after every selected file has been accounted for.
func ExtractSelectedExtFiles(options ExtractionOptions) (ExtractionReport, error) {
	report := ExtractionReport{APIVersion: "platform-factory.dev/legacy-extraction/v1", DiskPath: options.DiskPath, VolumeIndex: options.Volume.Index, Filesystem: options.Inventory.Filesystem, Output: options.Output, Complete: true, Extracted: []ExtractedFile{}, Excluded: []ExtractionExclusion{}, System: options.System}
	if options.Inventory.VolumeIndex != options.Volume.Index || !strings.HasPrefix(options.Inventory.Filesystem, "ext") {
		return report, fmt.Errorf("vmdisk: extraction requires the matching inventoried ext volume")
	}
	selected, err := validateExtractionSelection(options.SelectedPaths)
	if err != nil {
		return report, err
	}
	metadata := make(map[string]InventoryFile, len(options.Inventory.Files))
	for _, file := range options.Inventory.Files {
		metadata[file.Path] = file
	}
	if options.Inventory.Truncated || len(options.Inventory.UnsupportedFeatures) > 0 {
		report.Complete = false
		report.Excluded = append(report.Excluded, ExtractionExclusion{Path: "/", Reason: "filesystem inventory is truncated or has unsupported features"})
	}
	selectedSet := map[string]bool{}
	for _, selectedPath := range selected {
		selectedSet[selectedPath] = true
	}
	if options.System != nil {
		selectedService := false
		for _, service := range options.System.ServiceConfigurations {
			if !selectedSet[service.Source] && (options.SelectedEntrypoint == "" || service.Entrypoint != options.SelectedEntrypoint) {
				continue
			}
			selectedService = true
			for _, key := range service.SecretEnvironmentKeys {
				report.Complete = false
				report.Excluded = append(report.Excluded, ExtractionExclusion{Path: service.Source, Reason: "secret environment value " + key + " omitted"})
			}
			for _, reason := range service.IncompleteReasons {
				report.Complete = false
				report.Excluded = append(report.Excluded, ExtractionExclusion{Path: service.Source, Reason: reason})
			}
		}
		if selectedService && options.System.ServiceInspectionIncomplete {
			report.Complete = false
			report.Excluded = append(report.Excluded, ExtractionExclusion{Path: "/", Reason: "systemd service inspection exceeded its bounded limit"})
		}
	}
	for _, selectedPath := range selected {
		file, ok := metadata[selectedPath]
		if !ok {
			return report, fmt.Errorf("vmdisk: selected path %s is absent from the bounded inventory", selectedPath)
		}
		if file.Type != "file" {
			return report, fmt.Errorf("vmdisk: selected path %s is not a regular file", selectedPath)
		}
		if isSecretCandidate(selectedPath) && !options.IncludeSecrets {
			report.Complete = false
			report.Excluded = append(report.Excluded, ExtractionExclusion{Path: selectedPath, Reason: "probable secret excluded by default"})
			continue
		}
		if file.ExtendedAttributes {
			report.Complete = false
			report.Excluded = append(report.Excluded, ExtractionExclusion{Path: selectedPath, Reason: "extended attributes are reported but not preserved"})
		}
		report.Extracted = append(report.Extracted, ExtractedFile{Path: selectedPath, Mode: file.Mode & 0o7777, UID: file.UID, GID: file.GID, Size: file.Size, ExtendedAttributes: file.ExtendedAttributes})
		if file.Size > maxExtractedFile || report.TotalBytes > maxExtractedTotal-file.Size {
			return report, fmt.Errorf("vmdisk: selected extraction exceeds file or total byte limit")
		}
		report.TotalBytes += file.Size
	}
	if !report.Complete && !options.AllowIncomplete {
		return report, ErrIncompleteExtraction
	}
	report.ApprovedIncomplete = !report.Complete
	if err := installExtractedFiles(options, report); err != nil {
		return report, err
	}
	return report, nil
}

func validateExtractionSelection(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > maxExtractedFiles {
		return nil, fmt.Errorf("vmdisk: select between 1 and %d files", maxExtractedFiles)
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if value == "" || value == "/" || !strings.HasPrefix(value, "/") || path.Clean(value) != value || strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("vmdisk: invalid selected path %q", value)
		}
		if index > 0 && result[index-1] == value {
			return nil, fmt.Errorf("vmdisk: duplicate selected path %q", value)
		}
	}
	return result, nil
}

func isSecretCandidate(value string) bool {
	base := strings.ToLower(path.Base(value))
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "id_rsa" || base == "id_ed25519" {
		return true
	}
	words := strings.FieldsFunc(base, func(value rune) bool {
		return (value < 'a' || value > 'z') && (value < '0' || value > '9')
	})
	for _, word := range words {
		if word == "credentials" || word == "credential" || word == "secret" || word == "token" || word == "apikey" {
			return true
		}
	}
	return false
}

func installExtractedFiles(options ExtractionOptions, report ExtractionReport) (resultErr error) {
	if options.Output == "" {
		return fmt.Errorf("vmdisk: extraction output is required")
	}
	absOutput, err := filepath.Abs(options.Output)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(absOutput); !os.IsNotExist(err) {
		return fmt.Errorf("vmdisk: extraction output must not already exist")
	}
	parent := filepath.Dir(absOutput)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("vmdisk: extraction output parent must be an existing non-symlink directory")
	}
	temporary, err := os.MkdirTemp(parent, ".pf-legacy-extract-")
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, extracted := range report.Extracted {
		content, err := readExtFileWithLimit(options.DiskPath, options.Format, options.Volume, extracted.Path, maxExtractedFile)
		if err != nil {
			return fmt.Errorf("extract %s: %w", extracted.Path, err)
		}
		if uint64(len(content)) != extracted.Size {
			return fmt.Errorf("vmdisk: %s changed between inventory and extraction", extracted.Path)
		}
		destination := filepath.Join(temporary, filepath.FromSlash(strings.TrimPrefix(extracted.Path, "/")))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(destination, content, os.FileMode(extracted.Mode)); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(temporary, "extraction-report.json"), encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, absOutput); err != nil {
		return err
	}
	return nil
}

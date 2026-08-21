package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/vmdisk"
)

const (
	maxDiskParserRequest  = 64 << 10
	maxDiskParserResponse = 64 << 20
	maxDiskParserDisks    = 32
	maxDiskPathLength     = 4096
	diskParserTimeout     = 60 * time.Second
)

type diskParserRequest struct {
	Operation        string   `json:"operation,omitempty"`
	DiskImages       []string `json:"disk_images"`
	BootDiskOverride string   `json:"boot_disk_override,omitempty"`
	VolumeIndex      int      `json:"volume_index,omitempty"`
	SelectedPaths    []string `json:"selected_paths,omitempty"`
	Output           string   `json:"output,omitempty"`
	AllowIncomplete  bool     `json:"allow_incomplete,omitempty"`
	IncludeSecrets   bool     `json:"include_secrets,omitempty"`
	Entrypoint       string   `json:"entrypoint,omitempty"`
}

// runDiskParserWorker is deliberately not a public CLI command. It is the
// disposable process boundary around the parsers that consume untrusted disk
// bytes. Its stdout is one JSON document and diagnostics stay on stderr.
func runDiskParserWorker(stdin io.Reader, stdout, stderr io.Writer) int {
	debug.SetMemoryLimit(512 << 20)
	decoder := json.NewDecoder(io.LimitReader(stdin, maxDiskParserRequest+1))
	decoder.DisallowUnknownFields()
	var request diskParserRequest
	if err := decoder.Decode(&request); err != nil {
		fmt.Fprintf(stderr, "platform-factory disk parser: decode request: %v\n", err)
		return 2
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		fmt.Fprintln(stderr, "platform-factory disk parser: request must contain exactly one JSON document")
		return 2
	}
	if err := validateDiskParserRequest(request); err != nil {
		fmt.Fprintf(stderr, "platform-factory disk parser: %v\n", err)
		return 2
	}
	if request.Operation == "extract" {
		report, err := extractLegacyFilesInWorker(request)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory disk parser: %v\n", err)
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			fmt.Fprintf(stderr, "platform-factory disk parser: encode extraction report: %v\n", err)
			return 1
		}
		return 0
	}
	report, err := vmdisk.BuildDiscoveryReport(request.DiskImages, request.BootDiskOverride)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory disk parser: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintf(stderr, "platform-factory disk parser: encode report: %v\n", err)
		return 1
	}
	return 0
}

func validateDiskParserRequest(request diskParserRequest) error {
	if len(request.DiskImages) == 0 || len(request.DiskImages) > maxDiskParserDisks {
		return fmt.Errorf("disk count must be between 1 and %d", maxDiskParserDisks)
	}
	for _, path := range request.DiskImages {
		if strings.TrimSpace(path) == "" || len(path) > maxDiskPathLength || strings.IndexByte(path, 0) >= 0 {
			return fmt.Errorf("disk path is empty, too long, or contains NUL")
		}
	}
	if len(request.BootDiskOverride) > maxDiskPathLength || strings.IndexByte(request.BootDiskOverride, 0) >= 0 {
		return fmt.Errorf("boot disk override is too long or contains NUL")
	}
	if request.Operation != "" && request.Operation != "inspect" && request.Operation != "extract" {
		return fmt.Errorf("unsupported operation %q", request.Operation)
	}
	if request.Operation == "extract" {
		if len(request.DiskImages) != 1 || request.Output == "" || len(request.SelectedPaths) == 0 {
			return fmt.Errorf("extract requires exactly one disk, an output and selected paths")
		}
	}
	return nil
}

func extractLegacyFilesInWorker(request diskParserRequest) (vmdisk.ExtractionReport, error) {
	info, err := vmdisk.Detect(request.DiskImages[0])
	if err != nil {
		return vmdisk.ExtractionReport{}, err
	}
	volumeMap, err := vmdisk.ScanVolumeMap(request.DiskImages[0], info.Format)
	if err != nil {
		return vmdisk.ExtractionReport{}, err
	}
	var volume *vmdisk.Volume
	for index := range volumeMap.Volumes {
		if volumeMap.Volumes[index].Index == request.VolumeIndex {
			volume = &volumeMap.Volumes[index]
			break
		}
	}
	if volume == nil {
		return vmdisk.ExtractionReport{}, fmt.Errorf("volume index %d was not found", request.VolumeIndex)
	}
	inventory, err := vmdisk.InspectExtFilesystem(request.DiskImages[0], info.Format, *volume)
	if err != nil {
		return vmdisk.ExtractionReport{}, err
	}
	system, err := vmdisk.DetectLinuxSystem(request.DiskImages[0], info.Format, *volume, inventory)
	if err != nil {
		return vmdisk.ExtractionReport{}, err
	}
	return vmdisk.ExtractSelectedExtFiles(vmdisk.ExtractionOptions{DiskPath: request.DiskImages[0], Format: info.Format, Volume: *volume, Inventory: inventory, SelectedPaths: request.SelectedPaths, Output: request.Output, AllowIncomplete: request.AllowIncomplete, IncludeSecrets: request.IncludeSecrets, System: &system, SelectedEntrypoint: request.Entrypoint})
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		return 0, fmt.Errorf("output exceeds %d bytes", buffer.limit)
	}
	if len(value) > remaining {
		_, _ = buffer.Buffer.Write(value[:remaining])
		return remaining, fmt.Errorf("output exceeds %d bytes", buffer.limit)
	}
	return buffer.Buffer.Write(value)
}

func parseLegacyDisksIsolated(ctx context.Context, executable string, diskImages []string, bootDiskOverride string) (vmdisk.DiscoveryReport, error) {
	request := diskParserRequest{DiskImages: diskImages, BootDiskOverride: bootDiskOverride}
	if err := validateDiskParserRequest(request); err != nil {
		return vmdisk.DiscoveryReport{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return vmdisk.DiscoveryReport{}, fmt.Errorf("encode parser request: %w", err)
	}
	parserContext, cancel := context.WithTimeout(ctx, diskParserTimeout)
	defer cancel()
	command := exec.CommandContext(parserContext, executable, "__disk-parser")
	command.Stdin = bytes.NewReader(encoded)
	// Parsing needs no credentials, user configuration, PATH, HOME, proxy or
	// registry environment. A compromised parser therefore cannot obtain those
	// values from its process environment.
	command.Env = []string{"GOMEMLIMIT=512MiB"}
	var output boundedBuffer
	output.limit = maxDiskParserResponse
	var diagnostics boundedBuffer
	diagnostics.limit = maxDiskParserRequest
	command.Stdout = &output
	command.Stderr = &diagnostics
	if err := command.Run(); err != nil {
		if parserContext.Err() != nil {
			return vmdisk.DiscoveryReport{}, fmt.Errorf("isolated disk parser timed out after %s: %w", diskParserTimeout, parserContext.Err())
		}
		return vmdisk.DiscoveryReport{}, fmt.Errorf("isolated disk parser failed: %w: %s", err, strings.TrimSpace(diagnostics.String()))
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	var report vmdisk.DiscoveryReport
	if err := decoder.Decode(&report); err != nil {
		return vmdisk.DiscoveryReport{}, fmt.Errorf("decode isolated disk parser report: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return vmdisk.DiscoveryReport{}, fmt.Errorf("isolated disk parser returned trailing data")
	}
	if len(report.Disks) != len(diskImages) {
		return vmdisk.DiscoveryReport{}, fmt.Errorf("isolated disk parser returned %d disks for %d inputs", len(report.Disks), len(diskImages))
	}
	for index := range report.Disks {
		if report.Disks[index].Path != diskImages[index] {
			return vmdisk.DiscoveryReport{}, fmt.Errorf("isolated disk parser changed disk path at index %d", index)
		}
	}
	return report, nil
}

var parseLegacyDisksForCLI = func(ctx context.Context, diskImages []string, bootDiskOverride string) (vmdisk.DiscoveryReport, error) {
	executable, err := os.Executable()
	if err != nil {
		return vmdisk.DiscoveryReport{}, fmt.Errorf("resolve executable: %w", err)
	}
	return parseLegacyDisksIsolated(ctx, executable, diskImages, bootDiskOverride)
}

func extractLegacyFilesIsolated(ctx context.Context, executable string, request diskParserRequest) (vmdisk.ExtractionReport, error) {
	request.Operation = "extract"
	if err := validateDiskParserRequest(request); err != nil {
		return vmdisk.ExtractionReport{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return vmdisk.ExtractionReport{}, err
	}
	parserContext, cancel := context.WithTimeout(ctx, diskParserTimeout)
	defer cancel()
	command := exec.CommandContext(parserContext, executable, "__disk-parser")
	command.Stdin = bytes.NewReader(encoded)
	command.Env = []string{"GOMEMLIMIT=512MiB"}
	var output boundedBuffer
	output.limit = maxDiskParserResponse
	var diagnostics boundedBuffer
	diagnostics.limit = maxDiskParserRequest
	command.Stdout, command.Stderr = &output, &diagnostics
	if err := command.Run(); err != nil {
		if parserContext.Err() != nil {
			return vmdisk.ExtractionReport{}, fmt.Errorf("isolated disk extractor timed out after %s: %w", diskParserTimeout, parserContext.Err())
		}
		return vmdisk.ExtractionReport{}, fmt.Errorf("isolated disk extractor failed: %w: %s", err, strings.TrimSpace(diagnostics.String()))
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	var report vmdisk.ExtractionReport
	if err := decoder.Decode(&report); err != nil {
		return report, fmt.Errorf("decode isolated extraction report: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return report, fmt.Errorf("isolated disk extractor returned trailing data")
	}
	if report.DiskPath != request.DiskImages[0] || report.Output != request.Output || report.VolumeIndex != request.VolumeIndex {
		return report, fmt.Errorf("isolated disk extractor changed request identity")
	}
	return report, nil
}

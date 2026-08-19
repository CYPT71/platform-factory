// Package microvm is the application-layer service behind `pf microvm`'s
// one self-contained, sdk/cmd-independent business rule: mapping a
// microvm action to the KubeVirt plugin capability that implements it,
// and whether that capability mutates cluster state. Nearly everything
// else `pf microvm` does is already factored into internal/hypervisor,
// internal/microvm, internal/directboot, internal/vmdisk, and
// internal/networking (native KVM/HVF probing, direct boot, disk
// inspection, port-forward parsing); the remaining CLI code in
// cmd/platform-factory/microvm.go is deeply coupled to *pluginHost, the
// microVMExecutor process-launch abstraction, and flag parsing, and -
// per mvp.md's own explicit deferral - the native-KVM-eligibility
// hardware probing needs its own hermeticity design session before any
// further extraction there is safe. Only Capability, Params, and Result
// - the KubeVirt plugin's wire contract, with no host-probing or
// process-launch concerns at all - move here.
package microvm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/vmdisk"
)

// Params is the JSON wire shape platform-factory-kubevirt's own
// specParams (plugins/kubevirt/cmd/platform-factory-kubevirt/main.go)
// decodes - kept as an independent, explicitly JSON-tagged type here
// rather than a shared import, the same way any two independent RPC
// processes agree on a wire contract without sharing a Go type for it.
type Params struct {
	Name          string   `json:"name"`
	Namespace     string   `json:"namespace"`
	Image         string   `json:"image,omitempty"`
	Arch          string   `json:"arch,omitempty"`
	MemoryMiB     int      `json:"memory_mib,omitempty"`
	VCPUs         int      `json:"vcpus,omitempty"`
	ListenAddress string   `json:"listen_address,omitempty"`
	Publishes     []string `json:"publishes,omitempty"`
	Apply         bool     `json:"apply,omitempty"`
}

// Result is the union of platform-factory-kubevirt's manifestResult and
// commandResult - every field is optional because which ones a given
// capability populates differs (a lifecycle action returns Output;
// create and rbac return Manifest, and Applied/Output only when apply
// was set).
type Result struct {
	Manifest string `json:"manifest,omitempty"`
	Applied  bool   `json:"applied,omitempty"`
	Output   string `json:"output,omitempty"`
}

// Capability maps a microvm action to the runtime.* capability
// platform-factory-kubevirt declares for it, and whether that
// capability mutates cluster state - status and logs are pure
// observations; every other action can create, change or delete a real
// VirtualMachine and must go through an idempotency-protected call so a
// crash-and-retry can never repeat its effect, the same guarantee
// publish/deploy/rollback already give their own (non-plugin)
// mutations.
//
// The capability strings below are a hardcoded mirror of sdk/plugin's
// Capability* constants (CapabilityRuntimeCreate, ...) - this package
// cannot import sdk/ (see the package doc comment), so
// microvm_sdk_test.go cross-checks them against the real constants
// instead, in an external test package that's free to import sdk/plugin
// without that constraint applying to production code.
func Capability(action string) (capability string, mutating bool, err error) {
	switch action {
	case "create":
		return "runtime.create", true, nil
	case "start":
		return "runtime.start", true, nil
	case "stop":
		return "runtime.stop", true, nil
	case "restart":
		return "runtime.restart", true, nil
	case "delete":
		return "runtime.delete", true, nil
	case "status":
		return "runtime.status", false, nil
	case "logs":
		return "runtime.logs", false, nil
	case "rbac":
		return "runtime.rbac", true, nil
	default:
		return "", false, fmt.Errorf("unsupported kubevirt action %q", action)
	}
}

// ErrCompatibilityReport marks an InspectLegacyDisk failure that
// originates from vmdisk.BuildCompatibilityReport (an unrecognized
// --strategy, or a discovery digest it can't reconstruct) rather than
// disk discovery or report-file I/O - cmd maps it to its own "rejected
// request" exit code, distinct from the operational-failure code every
// other InspectLegacyDisk error gets.
var ErrCompatibilityReport = errors.New("legacy disk compatibility report")

// InspectLegacyDiskResult is the outcome of inspecting a set of legacy
// VM disk images: the discovery and compatibility reports, their
// rendered text, and the paths the four report files were written to
// under reportDir.
type InspectLegacyDiskResult struct {
	Report                vmdisk.DiscoveryReport
	Compatibility         vmdisk.CompatibilityReport
	Text                  string
	CompatibilityText     string
	JSONPath              string
	TextPath              string
	CompatibilityJSONPath string
	CompatibilityTextPath string
}

// InspectLegacyDisk builds a discovery report and a compatibility report
// for diskImages and writes both, as JSON and rendered text, under
// reportDir. It performs no CLI-facing validation (an empty diskImages
// is the caller's flag-parsing concern, not this function's) and never
// prints - callers present Result.Text/CompatibilityText and the report
// paths however their delivery mechanism requires.
func InspectLegacyDisk(diskImages []string, bootDiskOverride, reportDir string, strategy vmdisk.ExecutionMode) (InspectLegacyDiskResult, error) {
	report, err := vmdisk.BuildDiscoveryReport(diskImages, bootDiskOverride)
	if err != nil {
		return InspectLegacyDiskResult{}, err
	}
	compatibility, err := vmdisk.BuildCompatibilityReport(report, strategy)
	if err != nil {
		return InspectLegacyDiskResult{}, fmt.Errorf("%w: %w", ErrCompatibilityReport, err)
	}
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return InspectLegacyDiskResult{}, fmt.Errorf("create %s: %w", reportDir, err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return InspectLegacyDiskResult{}, fmt.Errorf("encode report: %w", err)
	}
	jsonPath := filepath.Join(reportDir, "discovery.json")
	if err := atomicfile.Write(reportDir, "discovery.json", encoded, 0o644, true); err != nil {
		return InspectLegacyDiskResult{}, fmt.Errorf("write %s: %w", jsonPath, err)
	}
	text := report.RenderText()
	textPath := filepath.Join(reportDir, "discovery.txt")
	if err := atomicfile.Write(reportDir, "discovery.txt", []byte(text), 0o644, true); err != nil {
		return InspectLegacyDiskResult{}, fmt.Errorf("write %s: %w", textPath, err)
	}
	compatibilityJSON, err := json.MarshalIndent(compatibility, "", "  ")
	if err != nil {
		return InspectLegacyDiskResult{}, fmt.Errorf("encode compatibility report: %w", err)
	}
	compatibilityJSONPath := filepath.Join(reportDir, "compatibility.json")
	if err := atomicfile.Write(reportDir, "compatibility.json", compatibilityJSON, 0o644, true); err != nil {
		return InspectLegacyDiskResult{}, fmt.Errorf("write %s: %w", compatibilityJSONPath, err)
	}
	compatibilityText := compatibility.RenderText()
	compatibilityTextPath := filepath.Join(reportDir, "compatibility.txt")
	if err := atomicfile.Write(reportDir, "compatibility.txt", []byte(compatibilityText), 0o644, true); err != nil {
		return InspectLegacyDiskResult{}, fmt.Errorf("write %s: %w", compatibilityTextPath, err)
	}
	return InspectLegacyDiskResult{
		Report:                report,
		Compatibility:         compatibility,
		Text:                  text,
		CompatibilityText:     compatibilityText,
		JSONPath:              jsonPath,
		TextPath:              textPath,
		CompatibilityJSONPath: compatibilityJSONPath,
		CompatibilityTextPath: compatibilityTextPath,
	}, nil
}

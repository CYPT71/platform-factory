package vmdisk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ExecutionMode is a product-level legacy migration outcome.
type ExecutionMode string

const (
	ModeAuto            ExecutionMode = "auto"
	ModeContainer       ExecutionMode = "container"
	ModeMicroVMOCI      ExecutionMode = "microvm-oci"
	ModeMicroVMDirect   ExecutionMode = "microvm-direct"
	ModeVMEncapsulation ExecutionMode = "vm-encapsulation"
	ModeUnsupported     ExecutionMode = "unsupported"
)

// CompatibilityReport explains what can safely be concluded from a discovery
// report. Empty evidence never becomes a positive compatibility claim.
type CompatibilityReport struct {
	APIVersion        string        `json:"api_version"`
	GeneratedAt       time.Time     `json:"generated_at"`
	DiscoveryDigest   string        `json:"discovery_digest"`
	RequestedStrategy ExecutionMode `json:"requested_strategy"`
	RecommendedMode   ExecutionMode `json:"recommended_mode"`
	Confidence        float64       `json:"confidence"`
	AutomaticDecision bool          `json:"automatic_decision"`
	DeploymentBlocked bool          `json:"deployment_blocked"`

	Reasons                []string         `json:"reasons"`
	AlternativeModes       []ModeAssessment `json:"alternative_modes"`
	LostFunctionality      []string         `json:"lost_functionality"`
	NetworkDifferences     []string         `json:"network_differences"`
	StorageDifferences     []string         `json:"storage_differences"`
	SecurityDifferences    []string         `json:"security_differences"`
	PerformanceDifferences []string         `json:"performance_differences"`
	BlockingConditions     []string         `json:"blocking_conditions"`
}

type ModeAssessment struct {
	Mode       ExecutionMode `json:"mode"`
	Eligible   bool          `json:"eligible"`
	Confidence float64       `json:"confidence"`
	Reason     string        `json:"reason"`
}

// BuildCompatibilityReport is fail-closed: today's header-only discovery can
// prove encapsulation eligibility, but cannot prove extraction or direct boot.
func BuildCompatibilityReport(discovery DiscoveryReport, strategy ExecutionMode) (CompatibilityReport, error) {
	if strategy == "" {
		strategy = ModeAuto
	}
	if !validStrategy(strategy) {
		return CompatibilityReport{}, fmt.Errorf("vmdisk: unknown compatibility strategy %q", strategy)
	}
	digest, err := discoveryDigest(discovery)
	if err != nil {
		return CompatibilityReport{}, err
	}
	report := CompatibilityReport{
		APIVersion:  "platform-factory.dev/vmdisk-compatibility/v1",
		GeneratedAt: time.Now().UTC(), DiscoveryDigest: digest, RequestedStrategy: strategy,
		RecommendedMode: ModeUnsupported, DeploymentBlocked: true,
		Reasons:                []string{"filesystem, operating-system, service and application inventory is unavailable, so extraction safety cannot be established"},
		LostFunctionality:      []string{"application-level health, lifecycle and dependency guarantees cannot be inferred from disk-container metadata"},
		NetworkDifferences:     []string{"guest network configuration and required ports are unknown; host forwarding must be reviewed explicitly"},
		StorageDifferences:     []string{"persistent-data boundaries are unknown; every attached disk must be treated as stateful"},
		SecurityDifferences:    []string{"encapsulation executes the source disk's untrusted bootloader, kernel and userspace rather than a minimized Platform Factory image"},
		PerformanceDifferences: []string{"virtualization overhead and workload resource requirements cannot be estimated from partition metadata"},
		BlockingConditions:     []string{"container, microvm-oci and microvm-direct require filesystem/kernel evidence that discovery does not currently provide"},
	}
	report.AlternativeModes = []ModeAssessment{
		{Mode: ModeContainer, Reason: "not eligible: container compatibility and safe OCI extraction are unknown"},
		{Mode: ModeMicroVMOCI, Reason: "not eligible: OCI extraction and required guest kernel capabilities are unknown"},
		{Mode: ModeMicroVMDirect, Reason: "not eligible: no trusted kernel/initramfs was discovered or pinned"},
		{Mode: ModeVMEncapsulation, Eligible: discovery.BootDiskResolved, Confidence: encapsulationConfidence(discovery), Reason: encapsulationReason(discovery)},
	}
	if strategy == ModeVMEncapsulation && discovery.BootDiskResolved {
		report.RecommendedMode = ModeVMEncapsulation
		report.Confidence = encapsulationConfidence(discovery)
		report.AutomaticDecision = false
		report.DeploymentBlocked = false
		report.Reasons = append(report.Reasons, "the user explicitly selected encapsulation and one boot disk was resolved from bounded MBR/GPT evidence")
		report.BlockingConditions = []string{}
	} else if strategy != ModeAuto {
		report.Reasons = append(report.Reasons, fmt.Sprintf("requested strategy %s cannot be proven safe from the available discovery evidence", strategy))
	}
	return report, nil
}

func validStrategy(mode ExecutionMode) bool {
	switch mode {
	case ModeAuto, ModeContainer, ModeMicroVMOCI, ModeMicroVMDirect, ModeVMEncapsulation, ModeUnsupported:
		return true
	default:
		return false
	}
}

func encapsulationConfidence(discovery DiscoveryReport) float64 {
	if discovery.BootDiskResolved {
		return 0.8
	}
	return 0
}

func encapsulationReason(discovery DiscoveryReport) string {
	if discovery.BootDiskResolved {
		return "eligible only by explicit choice: a boot disk is resolved and can be attached without extracting its contents"
	}
	return "not eligible: no unique boot disk was resolved; provide --boot-disk after human review"
}

func discoveryDigest(discovery DiscoveryReport) (string, error) {
	// Timestamps are presentation metadata, not discovery evidence.
	canonical := discovery
	canonical.GeneratedAt = time.Time{}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("vmdisk: encode discovery evidence: %w", err)
	}
	sum := sha256.Sum256(append([]byte("platform-factory/legacy-discovery/v1\x00"), data...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (r CompatibilityReport) RenderText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Legacy VM compatibility report - generated %s\n", r.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "Requested strategy: %s\nRecommended mode:  %s\nConfidence:        %.0f%%\nDeployment blocked: %v\n\n", r.RequestedStrategy, r.RecommendedMode, r.Confidence*100, r.DeploymentBlocked)
	b.WriteString("Why:\n")
	for _, reason := range r.Reasons {
		fmt.Fprintf(&b, "  - %s\n", reason)
	}
	b.WriteString("\nMode assessment:\n")
	for _, mode := range r.AlternativeModes {
		fmt.Fprintf(&b, "  - %s: %s\n", mode.Mode, mode.Reason)
	}
	if len(r.BlockingConditions) > 0 {
		b.WriteString("\nConditions preventing deployment:\n")
		for _, condition := range r.BlockingConditions {
			fmt.Fprintf(&b, "  - %s\n", condition)
		}
	}
	return b.String()
}

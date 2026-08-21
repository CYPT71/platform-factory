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

	Reasons                []string             `json:"reasons"`
	AlternativeModes       []ModeAssessment     `json:"alternative_modes"`
	LostFunctionality      []string             `json:"lost_functionality"`
	NetworkDifferences     []string             `json:"network_differences"`
	StorageDifferences     []string             `json:"storage_differences"`
	SecurityDifferences    []string             `json:"security_differences"`
	PerformanceDifferences []string             `json:"performance_differences"`
	BlockingConditions     []string             `json:"blocking_conditions"`
	Classification         LegacyClassification `json:"classification"`
}

type CompatibilityAssessment struct {
	Decision   string   `json:"decision"`
	Confidence float64  `json:"confidence"`
	Reason     string   `json:"reason"`
	Evidence   []string `json:"evidence"`
}

type LegacyClassification struct {
	ContainerCompatible     CompatibilityAssessment `json:"container_compatible"`
	DedicatedKernelRequired CompatibilityAssessment `json:"dedicated_kernel_required"`
	DeviceRequired          CompatibilityAssessment `json:"device_required"`
	NonExtractibleServices  CompatibilityAssessment `json:"non_extractible_services"`
	DataSeparable           CompatibilityAssessment `json:"data_separable"`
	SafeOCIExtraction       CompatibilityAssessment `json:"safe_oci_extraction"`
	MicroVMRequired         CompatibilityAssessment `json:"microvm_required"`
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
	report.Classification = classifyLegacyWorkload(discovery)
	report.AlternativeModes = []ModeAssessment{
		{Mode: ModeContainer, Reason: "not eligible: container compatibility and safe OCI extraction are unknown"},
		{Mode: ModeMicroVMOCI, Reason: "not eligible: OCI extraction and required guest kernel capabilities are unknown"},
		{Mode: ModeMicroVMDirect, Reason: "not eligible: no trusted kernel/initramfs was discovered or pinned"},
		{Mode: ModeVMEncapsulation, Eligible: discovery.BootDiskResolved, Confidence: encapsulationConfidence(discovery), Reason: encapsulationReason(discovery)},
	}
	report.applyClassification()
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

func classifyLegacyWorkload(discovery DiscoveryReport) LegacyClassification {
	unknown := func(reason string) CompatibilityAssessment {
		return CompatibilityAssessment{Decision: "unknown", Reason: reason, Evidence: []string{}}
	}
	c := LegacyClassification{
		ContainerCompatible:     unknown("no complete application inventory with a probable main process"),
		DedicatedKernelRequired: unknown("kernel requirements are not sufficiently inventoried"),
		DeviceRequired:          unknown("device requirements are not sufficiently inventoried"),
		NonExtractibleServices:  unknown("service lifecycle is not sufficiently inventoried"),
		DataSeparable:           unknown("persistent-data boundaries are not sufficiently inventoried"),
		SafeOCIExtraction:       unknown("container compatibility has not been established"),
		MicroVMRequired:         unknown("container and kernel requirements have not been established"),
	}
	filesystems, applications, systems := 0, 0, 0
	var devices, kernel, special, unresolved, services, secrets int
	mainProcesses, persistent := []string{}, []string{}
	for _, disk := range discovery.Disks {
		filesystems += len(disk.Filesystems)
		applications += len(disk.Applications)
		persistent = append(persistent, disk.PersistentData...)
		if disk.System != nil {
			systems++
			services += len(disk.System.Services) + len(disk.System.StartupFiles)
		}
		for _, app := range disk.Applications {
			devices += len(app.Devices)
			kernel += len(app.KernelModules)
			special += len(app.SpecialPaths)
			secrets += len(app.SecretCandidates)
			if app.MainProcess != nil {
				mainProcesses = append(mainProcesses, app.MainProcess.Path)
			}
			available := map[string]bool{}
			for _, lib := range app.SharedLibraries {
				available[pathBase(lib.Path)] = true
			}
			for _, dep := range app.ELFDependencies {
				if !available[dep.Path] {
					unresolved++
				}
			}
		}
	}
	if filesystems == 0 || applications == 0 || systems == 0 {
		return c
	}
	yes := func(reason string, evidence ...string) CompatibilityAssessment {
		return CompatibilityAssessment{"yes", .9, reason, evidence}
	}
	no := func(reason string, evidence ...string) CompatibilityAssessment {
		return CompatibilityAssessment{"no", .9, reason, evidence}
	}
	if devices > 0 {
		c.DeviceRequired = yes("device nodes are required", fmt.Sprintf("%d device finding(s)", devices))
	} else {
		c.DeviceRequired = no("no device node requirement was found", "complete filesystem inventory")
	}
	if kernel > 0 || special > 0 {
		c.DedicatedKernelRequired = yes("kernel-coupled dependencies were found", fmt.Sprintf("%d kernel module(s)", kernel), fmt.Sprintf("%d special-filesystem reference(s)", special))
	} else {
		c.DedicatedKernelRequired = no("no kernel module or special-filesystem dependency was found", "complete application inventory")
	}
	if services > len(mainProcesses) {
		c.NonExtractibleServices = yes("more system services/startup scripts exist than mapped main processes", fmt.Sprintf("%d lifecycle entry(s)", services), fmt.Sprintf("%d main process(es)", len(mainProcesses)))
	} else {
		c.NonExtractibleServices = no("all discovered lifecycle entries map to a probable main process", fmt.Sprintf("%d lifecycle entry(s)", services))
	}
	if len(persistent) > 0 {
		c.DataSeparable = yes("persistent mount boundaries were declared", persistent...)
	} else {
		c.DataSeparable = no("no persistent mount boundary was declared", "validated fstab inventory")
	}
	containerOK := len(mainProcesses) == 1 && devices == 0 && kernel == 0 && special == 0 && unresolved == 0 && c.NonExtractibleServices.Decision == "no"
	if containerOK {
		c.ContainerCompatible = yes("one main process and no kernel-coupled or unresolved runtime dependency were found", mainProcesses[0])
		if secrets == 0 {
			c.SafeOCIExtraction = yes("application files have a closed runtime dependency set, no probable secret and no privileged host coupling", mainProcesses[0])
			c.MicroVMRequired = no("the evidence supports standard container isolation", mainProcesses[0])
		} else {
			c.SafeOCIExtraction = no("probable secrets must be excluded or externalized before extraction", fmt.Sprintf("secret_candidates=%d", secrets))
			c.MicroVMRequired = yes("automatic OCI extraction is blocked until probable secrets are handled", fmt.Sprintf("secret_candidates=%d", secrets))
		}
	} else {
		evidence := []string{fmt.Sprintf("main_processes=%d", len(mainProcesses)), fmt.Sprintf("devices=%d", devices), fmt.Sprintf("kernel_modules=%d", kernel), fmt.Sprintf("special_paths=%d", special), fmt.Sprintf("unresolved_elf=%d", unresolved), fmt.Sprintf("secret_candidates=%d", secrets)}
		c.ContainerCompatible = no("container invariants are not all satisfied", evidence...)
		c.SafeOCIExtraction = no("safe OCI extraction requires container invariants and a closed dependency set", evidence...)
		c.MicroVMRequired = yes("kernel-coupled, device, lifecycle or unresolved dependency evidence prevents safe container extraction", evidence...)
	}
	return c
}

func pathBase(value string) string {
	if index := strings.LastIndexByte(value, '/'); index >= 0 {
		return value[index+1:]
	}
	return value
}

func (r *CompatibilityReport) applyClassification() {
	c := r.Classification
	if c.ContainerCompatible.Decision == "yes" && c.SafeOCIExtraction.Decision == "yes" {
		r.AlternativeModes[0] = ModeAssessment{Mode: ModeContainer, Eligible: true, Confidence: minConfidence(c.ContainerCompatible.Confidence, c.SafeOCIExtraction.Confidence), Reason: c.ContainerCompatible.Reason}
		if r.RequestedStrategy == ModeAuto || r.RequestedStrategy == ModeContainer {
			r.RecommendedMode, r.Confidence, r.AutomaticDecision, r.DeploymentBlocked = ModeContainer, r.AlternativeModes[0].Confidence, r.RequestedStrategy == ModeAuto, false
			r.Reasons = []string{c.ContainerCompatible.Reason, c.SafeOCIExtraction.Reason}
			r.BlockingConditions = []string{}
		}
	}
}

func minConfidence(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
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
	b.WriteString("\nEvidence classification:\n")
	for _, item := range []struct {
		name       string
		assessment CompatibilityAssessment
	}{
		{"container compatible", r.Classification.ContainerCompatible},
		{"dedicated kernel required", r.Classification.DedicatedKernelRequired},
		{"device required", r.Classification.DeviceRequired},
		{"non-extractible services", r.Classification.NonExtractibleServices},
		{"data separable", r.Classification.DataSeparable},
		{"safe OCI extraction", r.Classification.SafeOCIExtraction},
		{"MicroVM required", r.Classification.MicroVMRequired},
	} {
		fmt.Fprintf(&b, "  - %s: %s — %s\n", item.name, item.assessment.Decision, item.assessment.Reason)
	}
	if len(r.BlockingConditions) > 0 {
		b.WriteString("\nConditions preventing deployment:\n")
		for _, condition := range r.BlockingConditions {
			fmt.Fprintf(&b, "  - %s\n", condition)
		}
	}
	return b.String()
}

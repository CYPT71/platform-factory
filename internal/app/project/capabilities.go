package project

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/CYPT71/platform-factory/internal/hypervisor"
	"github.com/CYPT71/platform-factory/internal/hypervisor/sandbox"
	"github.com/CYPT71/platform-factory/internal/microvm"
	projectmodel "github.com/CYPT71/platform-factory/internal/project"
)

type CapabilityCheck struct {
	Name             string `json:"name"`
	Available        bool   `json:"available"`
	RequiredForBuild bool   `json:"required_for_build"`
	RequiredForRun   bool   `json:"required_for_run"`
	Detail           string `json:"detail,omitempty"`
	Remediation      string `json:"remediation,omitempty"`
}

type CapabilityAssessment struct {
	BuildReady bool              `json:"build_ready"`
	Isolation  string            `json:"isolation"`
	Platform   string            `json:"platform"`
	Checks     []CapabilityCheck `json:"checks"`
}

type capabilityProbe struct {
	stat         func(string) (os.FileInfo, error)
	lookPath     func(string) (string, error)
	probeNative  func(context.Context) (microvm.Capabilities, error)
	probeSandbox func() sandbox.Support
}

// AssessBuildCapabilities probes both the capabilities consumed by packaging
// now and those the selected isolation will consume later at run time. Only a
// missing build-time capability blocks pf build; deferred run capabilities are
// still made explicit instead of being silently assumed.
func AssessBuildCapabilities(ctx context.Context, loaded projectmodel.Loaded) (CapabilityAssessment, error) {
	return assessBuildCapabilities(ctx, loaded, capabilityProbe{
		stat: os.Stat, lookPath: exec.LookPath,
		probeNative: hypervisor.ProbeNative, probeSandbox: sandbox.ProbeSandbox,
	})
}

func assessBuildCapabilities(ctx context.Context, loaded projectmodel.Loaded, probe capabilityProbe) (CapabilityAssessment, error) {
	result := CapabilityAssessment{BuildReady: true, Isolation: loaded.Config.Isolation, Platform: loaded.Config.Platform, Checks: []CapabilityCheck{}}
	add := func(check CapabilityCheck) {
		result.Checks = append(result.Checks, check)
		if check.RequiredForBuild && !check.Available {
			result.BuildReady = false
		}
	}
	platformOK := loaded.Config.Platform == "linux/amd64" || loaded.Config.Platform == "linux/arm64"
	add(CapabilityCheck{Name: "oci-target", Available: platformOK, RequiredForBuild: true,
		Detail: loaded.Config.Platform, Remediation: "set platform to linux/amd64 or linux/arm64"})

	profile := strings.TrimSpace(loaded.Config.Profile)
	if profile != "" && interpretedProfile(profile) {
		runtimePath := strings.TrimSpace(loaded.Config.Runtime)
		available := false
		detail := runtimePath
		if runtimePath != "" {
			info, err := probe.stat(loaded.Resolve(runtimePath))
			available = err == nil && info.Mode().IsRegular()
			if err != nil {
				detail = err.Error()
			}
		}
		add(CapabilityCheck{Name: "language-runtime", Available: available, RequiredForBuild: true, Detail: detail,
			Remediation: "platform-factory does not fetch or build an interpreter implicitly; set runtime to a real packaged Linux interpreter, or run pf plugin provision-runtime"})
	}

	switch loaded.Config.Isolation {
	case "container":
		path, err := probe.lookPath(loaded.Config.RuntimeEngine)
		add(CapabilityCheck{Name: "container-runtime", Available: err == nil, RequiredForRun: true, Detail: path,
			Remediation: "install " + loaded.Config.RuntimeEngine + " and start its daemon before pf run"})
	case "microvm":
		capabilities, err := probe.probeNative(ctx)
		detail := capabilities.Details["unavailable"]
		if err != nil {
			detail = err.Error()
		}
		add(CapabilityCheck{Name: "native-hypervisor", Available: err == nil && capabilities.Available,
			RequiredForRun: true, Detail: detail,
			Remediation: "enable KVM, Hyper-V, or Virtualization.framework before pf run"})
		support := probe.probeSandbox()
		add(CapabilityCheck{Name: "vmm-sandbox-namespaces", Available: support.Namespaces, RequiredForRun: true,
			Detail: support.Details["namespaces"], Remediation: "grant namespace isolation support or use a supported native VMM host"})
		add(CapabilityCheck{Name: "vmm-sandbox-cgroups", Available: support.Cgroups, RequiredForRun: true,
			Detail: support.Details["cgroups"], Remediation: "enable writable cgroup v2 delegation for the VMM runtime"})
		add(CapabilityCheck{Name: "vmm-sandbox-capability-drop", Available: support.CapabilityBoundingDrop, RequiredForRun: true,
			Detail: support.Details["capability-bounding-drop"], Remediation: "run with permission to drop the VMM capability bounding set"})
	default:
		return result, fmt.Errorf("unsupported isolation %q", loaded.Config.Isolation)
	}
	if !result.BuildReady {
		for _, check := range result.Checks {
			if check.RequiredForBuild && !check.Available {
				return result, fmt.Errorf("required build capability %s is unavailable: %s", check.Name, check.Remediation)
			}
		}
	}
	return result, nil
}

func interpretedProfile(profile string) bool {
	switch profile {
	case "python", "node", "java", "dotnet", "ruby", "php":
		return true
	default:
		return false
	}
}

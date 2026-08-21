package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CYPT71/platform-factory/internal/hypervisor/sandbox"
	"github.com/CYPT71/platform-factory/internal/microvm"
)

func TestAssessBuildCapabilitiesSeparatesBuildAndDeferredContainerRun(t *testing.T) {
	loaded := loadTestProject(t, "version: 1\nlanguage: compiled\nprofile: static\nartifact: app\nisolation: container\nruntime_engine: docker\n")
	assessment, err := assessBuildCapabilities(context.Background(), loaded, capabilityProbe{
		stat:         os.Stat,
		lookPath:     func(string) (string, error) { return "", errors.New("missing") },
		probeNative:  func(context.Context) (microvm.Capabilities, error) { return microvm.Capabilities{}, nil },
		probeSandbox: func() sandbox.Support { return sandbox.Support{} },
	})
	if err != nil || !assessment.BuildReady {
		t.Fatalf("assessment=%+v err=%v", assessment, err)
	}
	if len(assessment.Checks) != 2 || assessment.Checks[1].Name != "container-runtime" || assessment.Checks[1].Available || assessment.Checks[1].RequiredForBuild || !assessment.Checks[1].RequiredForRun {
		t.Fatalf("checks=%+v", assessment.Checks)
	}
}

func TestAssessBuildCapabilitiesReportsMicroVMHypervisorAndSandboxWithoutBlockingPackaging(t *testing.T) {
	loaded := loadTestProject(t, "version: 1\nlanguage: compiled\nprofile: static\nartifact: app\nisolation: microvm\n")
	assessment, err := assessBuildCapabilities(context.Background(), loaded, capabilityProbe{
		stat: os.Stat, lookPath: func(string) (string, error) { return "", errors.New("missing") },
		probeNative: func(context.Context) (microvm.Capabilities, error) {
			return microvm.Capabilities{Available: false, Details: map[string]string{"unavailable": "virtualization disabled"}}, nil
		},
		probeSandbox: func() sandbox.Support {
			return sandbox.Support{Details: map[string]string{"namespaces": "not supported", "cgroups": "not supported", "capability-bounding-drop": "not supported"}}
		},
	})
	if err != nil || !assessment.BuildReady {
		t.Fatalf("assessment=%+v err=%v", assessment, err)
	}
	if len(assessment.Checks) != 5 {
		t.Fatalf("checks=%+v", assessment.Checks)
	}
	for _, check := range assessment.Checks[1:] {
		if check.Available || check.RequiredForBuild || !check.RequiredForRun {
			t.Fatalf("deferred check=%+v", check)
		}
	}
}

func TestAssessBuildCapabilitiesRequiresActualInterpretedRuntimeBeforeMutation(t *testing.T) {
	loaded := loadTestProject(t, "version: 1\nlanguage: python\nprofile: python\nartifact: app.py\nruntime: runtime/python\n")
	probe := capabilityProbe{
		stat: os.Stat, lookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		probeNative:  func(context.Context) (microvm.Capabilities, error) { return microvm.Capabilities{}, nil },
		probeSandbox: func() sandbox.Support { return sandbox.Support{} },
	}
	assessment, err := assessBuildCapabilities(context.Background(), loaded, probe)
	if err == nil || assessment.BuildReady {
		t.Fatalf("missing runtime assessment=%+v err=%v", assessment, err)
	}
	runtimePath := filepath.Join(loaded.Root, "runtime", "python")
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimePath, []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	assessment, err = assessBuildCapabilities(context.Background(), loaded, probe)
	if err != nil || !assessment.BuildReady {
		t.Fatalf("present runtime assessment=%+v err=%v", assessment, err)
	}
}

package main

import (
	"fmt"
	"runtime"
	"strings"
)

// Pure catalog/selection logic, kept separate from main.go's exec/TUI glue
// so it stays unit-testable and visible to the coverage gate; see main_test.go.

type component struct {
	key         string
	label       string
	description string
	binaries    []string
	mandatory   bool
}

var components = []component{
	{
		key:         "core",
		label:       "CLI de base",
		description: "platform-factory (alias pf) : launch, build, pipeline, sbom, plugins, diff",
		binaries:    []string{"platform-factory"},
		mandatory:   true,
	},
	{
		key:         "builder",
		label:       "OCI Builder",
		description: "oci-builder : construit une image OCI de façon autonome",
		binaries:    []string{"oci-builder"},
	},
	{
		key:         "microvm",
		label:       "Support MicroVM",
		description: "microvm-init + microvm-initramfs + platform-factory-runtime : exécution isolée en microVM (KVM/HVF), utilisable comme runtime OCI par Podman/Docker/containerd",
		binaries:    []string{"microvm-init", "microvm-initramfs", "platform-factory-runtime"},
	},
	{
		key:         "distributed",
		label:       "Plateforme distribuée",
		description: "platform-factory-control-plane + platform-factory-worker : orchestration multi-nœuds",
		binaries:    []string{"platform-factory-control-plane", "platform-factory-worker"},
	},
}

func parseComponents(csv string) []string {
	var keys []string
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			keys = append(keys, part)
		}
	}
	return keys
}

func resolveComponents(keys []string) ([]component, error) {
	known := make(map[string]component, len(components))
	for _, c := range components {
		known[c.key] = c
	}
	requested := map[string]bool{"core": true}
	for _, k := range keys {
		if _, ok := known[k]; !ok {
			return nil, fmt.Errorf("unknown component %q (use -list to see available components)", k)
		}
		requested[k] = true
	}
	var selected []component
	for _, c := range components {
		if requested[c.key] {
			selected = append(selected, c)
		}
	}
	return selected, nil
}

type buildStep struct {
	name   string
	pkg    string
	cgo    bool
	status buildStatus
	err    error
}

func buildSteps(selected []component, goos, goarch string) []buildStep {
	hostMatch := goos == runtime.GOOS && goarch == runtime.GOARCH
	nativeMacVMM := hostMatch && goos == "darwin"
	var steps []buildStep
	for _, c := range selected {
		for _, name := range c.binaries {
			steps = append(steps, buildStep{
				name: name,
				pkg:  "./cmd/" + name,
				cgo:  name == "platform-factory" && nativeMacVMM,
			})
		}
	}
	return steps
}

func binSuffix(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

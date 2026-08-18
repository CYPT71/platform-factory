// Package installer is the application-layer service behind
// cmd/platform-factory-installer: the component catalog, key parsing
// and resolution, and the build-step plan derived from a selection.
// cmd/platform-factory-installer/main.go only handles the TUI/plain-
// fallback presentation and the one place that actually shells out to
// `go build` - every catalog/selection decision lives here, testable
// without a terminal at all.
package installer

import (
	"fmt"
	"runtime"
	"strings"
)

// Component is one selectable unit of the installer: a labeled group
// of binaries built and installed together.
type Component struct {
	Key         string
	Label       string
	Description string
	Binaries    []string
	Mandatory   bool
}

// Components is the installer's full catalog, in display/build order.
var Components = []Component{
	{
		Key:         "core",
		Label:       "CLI de base",
		Description: "platform-factory (alias pf) : launch, build, pipeline, sbom, plugins, diff",
		Binaries:    []string{"platform-factory"},
		Mandatory:   true,
	},
	{
		Key:         "builder",
		Label:       "OCI Builder",
		Description: "oci-builder : construit une image OCI de façon autonome",
		Binaries:    []string{"oci-builder"},
	},
	{
		Key:         "microvm",
		Label:       "Support MicroVM",
		Description: "microvm-init + microvm-initramfs + platform-factory-runtime : exécution isolée en microVM (KVM/HVF), utilisable comme runtime OCI par Podman/Docker/containerd",
		Binaries:    []string{"microvm-init", "microvm-initramfs", "platform-factory-runtime"},
	},
	{
		Key:         "distributed",
		Label:       "Plateforme distribuée",
		Description: "platform-factory-control-plane + platform-factory-worker : orchestration multi-nœuds",
		Binaries:    []string{"platform-factory-control-plane", "platform-factory-worker"},
	},
}

// ParseComponents splits a comma-separated -components flag value into
// its (trimmed, non-empty) keys.
func ParseComponents(csv string) []string {
	var keys []string
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			keys = append(keys, part)
		}
	}
	return keys
}

// ResolveComponents validates keys against Components and returns the
// selected set in catalog order, deduplicated, with "core" always
// included regardless of whether it was requested explicitly.
func ResolveComponents(keys []string) ([]Component, error) {
	known := make(map[string]Component, len(Components))
	for _, c := range Components {
		known[c.Key] = c
	}
	requested := map[string]bool{"core": true}
	for _, k := range keys {
		if _, ok := known[k]; !ok {
			return nil, fmt.Errorf("unknown component %q (use -list to see available components)", k)
		}
		requested[k] = true
	}
	var selected []Component
	for _, c := range Components {
		if requested[c.Key] {
			selected = append(selected, c)
		}
	}
	return selected, nil
}

// BuildStep is one binary to build: its name, package path, and
// whether it needs CGO.
type BuildStep struct {
	Name string
	Pkg  string
	CGO  bool
}

// BuildSteps flattens selected's binaries into the ordered list of
// builds it implies. CGO is enabled only for platform-factory itself,
// and only when building natively for darwin (its own native macOS VMM
// support needs cgo; a cross-compiled or non-macOS build never does).
func BuildSteps(selected []Component, goos, goarch string) []BuildStep {
	hostMatch := goos == runtime.GOOS && goarch == runtime.GOARCH
	nativeMacVMM := hostMatch && goos == "darwin"
	var steps []BuildStep
	for _, c := range selected {
		for _, name := range c.Binaries {
			steps = append(steps, BuildStep{
				Name: name,
				Pkg:  "./cmd/" + name,
				CGO:  name == "platform-factory" && nativeMacVMM,
			})
		}
	}
	return steps
}

// BinSuffix is the executable file suffix for goos ("" everywhere but
// Windows).
func BinSuffix(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

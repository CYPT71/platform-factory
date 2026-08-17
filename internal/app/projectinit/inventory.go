package projectinit

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	api "github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/scheduler"
)

const InventoryAPIVersion = "platform-factory.dev/project-inventory/v1"

type ProjectInventory struct {
	APIVersion string               `json:"api_version"`
	Primary    string               `json:"primary"`
	Ecosystems []EcosystemInventory `json:"ecosystems"`
}

func renderInitialBuildDAG(ecosystem Ecosystem) ([]byte, error) {
	inspection := ecosystem.Inspection
	if len(inspection.BuildCommand) == 0 {
		for _, candidate := range ecosystem.Inspections {
			if candidate.Detection.Kind == ecosystem.Result.Kind {
				inspection = candidate
				break
			}
		}
	}
	command := api.Command{Executable: "/bin/true"}
	stageID := "assemble"
	if len(inspection.BuildCommand) > 0 {
		command.Executable = inspection.BuildCommand[0]
		command.Args = append([]string(nil), inspection.BuildCommand[1:]...)
		stageID = "build"
	}
	definition := api.Pipeline{
		APIVersion: api.PipelineAPIVersion,
		Name:       "project-build",
		Stages: []api.Stage{{
			ID: stageID, Command: command, Network: api.NetworkNone,
		}},
	}
	if _, err := scheduler.Analyze(definition); err != nil {
		return nil, fmt.Errorf("validate initial build DAG: %w", err)
	}
	encoded, err := json.MarshalIndent(definition, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode initial build DAG: %w", err)
	}
	return append(encoded, '\n'), nil
}

type EcosystemInventory struct {
	Language     string               `json:"language"`
	Profile      string               `json:"profile,omitempty"`
	Selected     bool                 `json:"selected"`
	Evidence     []string             `json:"evidence"`
	Toolchain    ToolchainInventory   `json:"toolchain"`
	Dependencies DependencyInventory  `json:"dependencies"`
	Application  ApplicationInventory `json:"application"`
	Metadata     MetadataInventory    `json:"metadata"`
	Unknowns     []Unknown            `json:"unknowns,omitempty"`
}

type ToolchainInventory struct {
	Name         string   `json:"name"`
	Profile      string   `json:"profile,omitempty"`
	BuildCommand []string `json:"build_command,omitempty"`
	Version      string   `json:"version"`
}

type DependencyInventory struct {
	Mode     string   `json:"mode"`
	Manifest string   `json:"manifest,omitempty"`
	Imports  []string `json:"imports,omitempty"`
	Reason   string   `json:"reason"`
}

type ApplicationInventory struct {
	Artifact   string `json:"artifact,omitempty"`
	Entrypoint string `json:"entrypoint,omitempty"`
}

type MetadataInventory struct {
	Ports           []string `json:"ports,omitempty"`
	EnvironmentKeys []string `json:"environment_keys,omitempty"`
	Storage         []string `json:"storage,omitempty"`
}

func renderInventory(ecosystem Ecosystem) ([]byte, error) {
	document := ProjectInventory{APIVersion: InventoryAPIVersion, Primary: ecosystem.Result.Kind}
	for _, inspection := range ecosystem.Inspections {
		keys := make([]string, 0, len(inspection.Environment))
		for key := range inspection.Environment {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		item := EcosystemInventory{
			Language: inspection.Detection.Kind, Profile: inspection.Detection.Profile,
			Selected:     inspection.Detection.Kind == ecosystem.Result.Kind,
			Evidence:     append([]string(nil), inspection.Detection.Evidence...),
			Toolchain:    ToolchainInventory{Name: inspection.Detection.Kind, Profile: inspection.Detection.Profile, BuildCommand: append([]string(nil), inspection.BuildCommand...), Version: "unknown"},
			Dependencies: DependencyInventory{Mode: inspection.Dependencies.Mode, Manifest: inspection.Dependencies.Manifest, Imports: append([]string(nil), inspection.Dependencies.Imports...), Reason: inspection.Dependencies.Reason},
			Application:  ApplicationInventory{Artifact: inspection.Artifact, Entrypoint: inspection.Entrypoint},
			Metadata:     MetadataInventory{Ports: append([]string(nil), inspection.Ports...), EnvironmentKeys: keys, Storage: append([]string(nil), inspection.Storage...)},
			Unknowns:     append([]Unknown(nil), inspection.Unknowns...),
		}
		item.Unknowns = append(item.Unknowns, Unknown{Subject: "toolchain.version", Reason: "language plugin did not report an exact version"})
		if err := validateInventoryItem(item); err != nil {
			return nil, err
		}
		document.Ecosystems = append(document.Ecosystems, item)
	}
	slices.SortFunc(document.Ecosystems, func(a, b EcosystemInventory) int { return strings.Compare(a.Language, b.Language) })
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode project inventory: %w", err)
	}
	return append(encoded, '\n'), nil
}

func validateInventoryItem(item EcosystemInventory) error {
	if item.Language == "" || len(item.Language) > 64 || strings.ContainsAny(item.Language, "\x00\r\n") {
		return errors.New("project inventory: plugin returned an invalid language")
	}
	if len(item.Evidence) > 1024 || len(item.Dependencies.Imports) > 4096 || len(item.Metadata.EnvironmentKeys) > 4096 {
		return errors.New("project inventory: plugin observation exceeds object limits")
	}
	paths := append([]string(nil), item.Evidence...)
	paths = append(paths, item.Dependencies.Manifest, item.Application.Artifact, item.Application.Entrypoint)
	for _, value := range paths {
		if value == "" {
			continue
		}
		clean := filepath.Clean(value)
		if filepath.IsAbs(value) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.ContainsRune(value, 0) {
			return fmt.Errorf("project inventory: plugin returned unsafe project path %q", value)
		}
	}
	for _, key := range item.Metadata.EnvironmentKeys {
		if key == "" || len(key) > 256 || strings.ContainsAny(key, "=\x00\r\n") {
			return errors.New("project inventory: invalid environment key")
		}
	}
	return nil
}

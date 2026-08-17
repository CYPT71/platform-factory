package projectinit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/CYPT71/platform-factory/internal/detect"
)

type DependencyState struct {
	Mode, Manifest, Reason string
	Imports                []string
}
type ApplicationInspection struct {
	Detection            detect.Result
	BuildCommand         []string
	Artifact, Entrypoint string
	Ports                []string
	Environment          map[string]string
	Storage              []string
	Dependencies         DependencyState
	Runtime              RuntimeDecision
	Unknowns             []Unknown
}

// EnrichOperationalHints adds language-neutral evidence to a language
// plugin's inspection. It never classifies source files or infers a build.
func EnrichOperationalHints(dir string, inspection ApplicationInspection) ApplicationInspection {
	inspection.Ports, inspection.Environment, inspection.Storage = inspectOperationalHints(dir)
	inspection.Runtime = RuntimeDecision{Recommended: RuntimeContainer, Reasons: []string{"the language plugin reported an application source or artifact"}, Unknowns: []Unknown{{Subject: "runtime.selected", Reason: "operator confirmation required"}}}
	if inspection.Artifact == "" {
		inspection.Unknowns = append(inspection.Unknowns, Unknown{Subject: "build.artifact", Reason: "the selected language plugin did not prove an artifact or entrypoint"})
	}
	if inspection.Dependencies.Mode == "unknown" || inspection.Dependencies.Mode == "unresolved" {
		inspection.Unknowns = append(inspection.Unknowns, Unknown{Subject: "dependencies", Reason: inspection.Dependencies.Reason})
	}
	return inspection
}

func inspectOperationalHints(dir string) ([]string, map[string]string, []string) {
	ports := []string{}
	env := map[string]string{}
	storage := []string{}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := strings.ToLower(entry.Name())
		if name == ".env.example" || name == ".env.sample" {
			file, err := os.Open(filepath.Join(dir, entry.Name()))
			if err == nil {
				scanner := bufio.NewScanner(file)
				for scanner.Scan() {
					line := strings.TrimSpace(scanner.Text())
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					key, value, ok := strings.Cut(line, "=")
					if ok {
						env[key] = value
					}
				}
				file.Close()
			}
		}
		if strings.Contains(name, "docker-compose") && (strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			raw, _ := os.ReadFile(filepath.Join(dir, entry.Name()))
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.Trim(strings.TrimSpace(line), `- "'`)
				if left, right, ok := strings.Cut(line, ":"); ok {
					if _, e1 := strconv.Atoi(left); e1 == nil {
						if _, e2 := strconv.Atoi(strings.TrimSpace(right)); e2 == nil {
							ports = append(ports, left+":"+strings.TrimSpace(right))
						}
					}
				}
			}
		}
		if name == "dockerfile" {
			raw, _ := os.ReadFile(filepath.Join(dir, entry.Name()))
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && strings.EqualFold(fields[0], "EXPOSE") {
					ports = append(ports, fields[1]+":"+fields[1])
				}
			}
		}
	}
	for _, name := range []string{"data", "storage", "uploads"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.IsDir() {
			storage = append(storage, name)
		}
	}
	slices.Sort(ports)
	ports = slices.Compact(ports)
	slices.Sort(storage)
	return ports, env, storage
}

func (i ApplicationInspection) Descriptions() []string {
	if i.Detection.Kind == "" {
		return nil
	}
	lines := []string{fmt.Sprintf("language %s (plugin evidence: %s)", i.Detection.Kind, strings.Join(i.Detection.Evidence, ", ")), "dependencies " + i.Dependencies.Mode + ": " + i.Dependencies.Reason}
	if len(i.BuildCommand) > 0 {
		encoded, _ := json.Marshal(i.BuildCommand)
		lines = append(lines, "build command argv "+string(encoded))
	}
	if i.Artifact != "" {
		lines = append(lines, "artifact "+i.Artifact)
	}
	if i.Runtime.Recommended != "" {
		lines = append(lines, "recommended runtime "+string(i.Runtime.Recommended)+" ("+strings.Join(i.Runtime.Reasons, ", ")+")")
	}
	if len(i.Ports) > 0 {
		lines = append(lines, "ports "+strings.Join(i.Ports, ", "))
	}
	if len(i.Environment) > 0 {
		keys := make([]string, 0, len(i.Environment))
		for key := range i.Environment {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		lines = append(lines, "environment "+strings.Join(keys, ", "))
	}
	if len(i.Storage) > 0 {
		lines = append(lines, "storage "+strings.Join(i.Storage, ", "))
	}
	for _, unknown := range i.Unknowns {
		lines = append(lines, unknown.Description())
	}
	return lines
}

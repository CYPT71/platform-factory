package langplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type DependencyInspection struct {
	Mode     string   `json:"mode"`
	Manifest string   `json:"manifest,omitempty"`
	Imports  []string `json:"imports,omitempty"`
	Reason   string   `json:"reason"`
}

// Inspection is the language-owned, read-only answer consumed by pf init.
// Match=false means the host should consult another plugin.
type Inspection struct {
	Match        bool                 `json:"match"`
	Language     string               `json:"language,omitempty"`
	Profile      string               `json:"profile,omitempty"`
	Evidence     []string             `json:"evidence,omitempty"`
	BuildCommand []string             `json:"build_command,omitempty"`
	Artifact     string               `json:"artifact,omitempty"`
	Entrypoint   string               `json:"entrypoint,omitempty"`
	Dependencies DependencyInspection `json:"dependencies"`
}

// Definition belongs in a language plugin. The SDK supplies only safe,
// deterministic filesystem mechanics; it contains no language catalogue.
type Definition struct {
	Language         string
	Profile          string
	Markers          []string
	SourceExtensions []string
	Entrypoints      []string
	Manifests        []string
	Infer            func(root string, sources []string) (build, artifact string)
	Imports          func(source string) (external []string, dynamic bool)
}

func Inspect(root string, definition Definition) (Inspection, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return Inspection{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return Inspection{}, errors.New("inspection root must be a real directory")
	}
	result := Inspection{Language: definition.Language, Profile: definition.Profile}
	for _, marker := range definition.Markers {
		if matches, _ := filepath.Glob(filepath.Join(root, marker)); len(matches) > 0 {
			sort.Strings(matches)
			result.Evidence = append(result.Evidence, filepath.Base(matches[0]))
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return Inspection{}, err
	}
	var sources []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		for _, ext := range definition.SourceExtensions {
			if strings.EqualFold(filepath.Ext(entry.Name()), ext) {
				sources = append(sources, entry.Name())
				break
			}
		}
	}
	sort.Strings(sources)
	if len(result.Evidence) == 0 && len(sources) > 0 {
		result.Evidence = append(result.Evidence, sources...)
	}
	if len(result.Evidence) == 0 {
		return Inspection{Match: false}, nil
	}
	result.Match = true
	for _, manifest := range definition.Manifests {
		if matches, _ := filepath.Glob(filepath.Join(root, manifest)); len(matches) > 0 {
			sort.Strings(matches)
			name := filepath.Base(matches[0])
			result.Dependencies = DependencyInspection{Mode: "manifest", Manifest: name, Reason: name + " detected"}
			break
		}
	}
	if result.Dependencies.Mode == "" {
		var imports []string
		dynamic := false
		for _, name := range sources {
			raw, readErr := os.ReadFile(filepath.Join(root, name))
			if readErr != nil {
				return Inspection{}, readErr
			}
			if definition.Imports != nil {
				found, isDynamic := definition.Imports(string(raw))
				imports = append(imports, found...)
				dynamic = dynamic || isDynamic
			}
		}
		sort.Strings(imports)
		imports = compact(imports)
		switch {
		case dynamic:
			result.Dependencies = DependencyInspection{Mode: "unknown", Imports: imports, Reason: "dynamic module loading detected"}
		case len(imports) > 0:
			result.Dependencies = DependencyInspection{Mode: "unresolved", Imports: imports, Reason: "external imports detected without a dependency manifest"}
		default:
			result.Dependencies = DependencyInspection{Mode: "none", Reason: "no external dependencies detected"}
		}
	}
	for _, candidate := range definition.Entrypoints {
		if fileExists(filepath.Join(root, candidate)) {
			result.Entrypoint = candidate
			result.Artifact = candidate
			break
		}
	}
	if definition.Infer != nil {
		build, artifact := definition.Infer(root, sources)
		if build != "" {
			result.BuildCommand = strings.Split(build, "\x00")
		}
		if artifact != "" {
			result.Artifact = artifact
		}
	}
	return result, nil
}

func WriteInspection(result Inspection) error { return json.NewEncoder(os.Stdout).Encode(result) }

func RunInspection(binary, root string) (Inspection, error) {
	cmd := exec.Command(binary, "inspect", "--root", root)
	output, err := cmd.Output()
	if err != nil {
		return Inspection{}, fmt.Errorf("%s inspect: %w", binary, err)
	}
	var result Inspection
	if err := json.Unmarshal(output, &result); err != nil {
		return Inspection{}, fmt.Errorf("decode %s inspection: %w", binary, err)
	}
	return result, nil
}

func InspectLoaded(root string) ([]Inspection, error) {
	names, err := List()
	if err != nil {
		return nil, err
	}
	var results []Inspection
	for _, name := range names {
		binary, err := Resolve(name)
		if err != nil {
			return nil, err
		}
		result, err := RunInspection(binary, root)
		if err != nil {
			return nil, err
		}
		if result.Match {
			results = append(results, result)
		}
	}
	return results, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
func compact(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

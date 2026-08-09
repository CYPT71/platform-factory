// Package detect classifies application inputs without executing them.
package detect

import (
	"archive/zip"
	"bufio"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Result struct {
	Path         string   `json:"path"`
	Kind         string   `json:"kind"`
	Profile      string   `json:"profile"`
	Architecture string   `json:"architecture,omitempty"`
	Interpreter  string   `json:"interpreter,omitempty"`
	NativeDeps   []string `json:"native_dependencies,omitempty"`
	Evidence     []string `json:"evidence"`
	Ambiguous    bool     `json:"ambiguous"`
	Candidates   []string `json:"candidates,omitempty"`
}

func Path(name string) (Result, error) {
	info, err := os.Stat(name)
	if err != nil {
		return Result{}, fmt.Errorf("stat input: %w", err)
	}
	if info.IsDir() {
		return directory(name)
	}
	if !info.Mode().IsRegular() {
		return Result{}, errors.New("input must be a regular file or directory")
	}
	if result, ok, err := elfFile(name); ok || err != nil {
		return result, err
	}
	if result, ok, err := scriptFile(name); ok || err != nil {
		return result, err
	}
	if strings.EqualFold(filepath.Ext(name), ".jar") {
		if reader, err := zip.OpenReader(name); err == nil {
			reader.Close()
			return Result{Path: name, Kind: "java", Profile: "java", Evidence: []string{"zip-compatible .jar archive"}}, nil
		}
	}
	if strings.EqualFold(filepath.Ext(name), ".dll") {
		return Result{Path: name, Kind: "dotnet", Profile: "dotnet", Evidence: []string{".dll extension"}}, nil
	}
	return Result{Path: name, Kind: "unknown", Profile: "unknown", Evidence: []string{"no recognized executable signature"}}, nil
}

func directory(name string) (Result, error) {
	checks := []struct {
		kind, profile string
		files         []string
	}{
		{"node", "node", []string{"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock"}},
		{"python", "python", []string{"requirements.lock", "pyproject.toml", "Pipfile.lock"}},
		{"java", "java", []string{"pom.xml", "gradlew", "build.gradle", "build.gradle.kts"}},
		{"dotnet", "dotnet", []string{"global.json"}},
		// Compiled ecosystems produce ELF executables, so their runtime
		// profile is resolved from the built binary, not the language.
		{"go", "static", []string{"go.mod"}},
		{"rust", "static", []string{"Cargo.lock", "Cargo.toml"}},
		{"ruby", "ruby", []string{"Gemfile.lock", "Gemfile"}},
		{"php", "php", []string{"composer.lock", "composer.json"}},
	}
	var candidates, evidence []string
	profiles := map[string]string{}
	for _, check := range checks {
		for _, file := range check.files {
			if info, err := os.Stat(filepath.Join(name, file)); err == nil && info.Mode().IsRegular() {
				candidates = append(candidates, check.kind)
				profiles[check.kind] = check.profile
				evidence = append(evidence, file)
				break
			}
		}
	}
	sort.Strings(candidates)
	sort.Strings(evidence)
	if len(candidates) == 0 {
		return Result{Path: name, Kind: "unknown", Profile: "unknown", Evidence: []string{"no supported lockfile or project marker"}}, nil
	}
	result := Result{Path: name, Kind: candidates[0], Profile: profiles[candidates[0]], Evidence: evidence, Candidates: candidates}
	if len(candidates) > 1 {
		result.Kind, result.Profile, result.Ambiguous = "ambiguous", "unknown", true
	}
	return result, nil
}

func elfFile(name string) (Result, bool, error) {
	file, err := elf.Open(name)
	if err != nil {
		var format *elf.FormatError
		if errors.As(err, &format) || errors.Is(err, io.EOF) {
			return Result{}, false, nil
		}
		return Result{}, true, err
	}
	defer file.Close()
	architecture := map[elf.Machine]string{elf.EM_X86_64: "amd64", elf.EM_AARCH64: "arm64"}[file.Machine]
	if architecture == "" {
		architecture = file.Machine.String()
	}
	interpreter := ""
	for _, program := range file.Progs {
		if program.Type == elf.PT_INTERP {
			data, readErr := io.ReadAll(io.LimitReader(program.Open(), 4096))
			if readErr != nil {
				return Result{}, true, readErr
			}
			interpreter = strings.TrimRight(string(data), "\x00")
		}
	}
	needed, _ := file.DynString(elf.DT_NEEDED)
	sort.Strings(needed)
	profile := "static"
	if strings.Contains(interpreter, "ld-musl") {
		profile = "musl"
	} else if interpreter != "" || len(needed) > 0 {
		profile = "glibc"
	}
	return Result{Path: name, Kind: "elf", Profile: profile, Architecture: architecture, Interpreter: interpreter, NativeDeps: needed, Evidence: []string{"ELF header"}}, true, nil
}

func scriptFile(name string) (Result, bool, error) {
	file, err := os.Open(name)
	if err != nil {
		return Result{}, true, err
	}
	defer file.Close()
	line, err := bufio.NewReader(io.LimitReader(file, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{}, true, err
	}
	if !strings.HasPrefix(line, "#!") {
		return Result{}, false, nil
	}
	shebang := strings.TrimSpace(strings.TrimPrefix(line, "#!"))
	kind := "script"
	profile := "unknown"
	switch {
	case strings.Contains(shebang, "python"):
		kind, profile = "python", "python"
	case strings.Contains(shebang, "node"):
		kind, profile = "node", "node"
	case strings.Contains(shebang, "ruby"):
		kind, profile = "ruby", "ruby"
	case strings.Contains(shebang, "php"):
		kind, profile = "php", "php"
	}
	return Result{Path: name, Kind: kind, Profile: profile, Interpreter: shebang, Evidence: []string{"shebang"}}, true, nil
}

func JSON(result Result) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}

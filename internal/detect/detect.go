// Package detect classifies application inputs without executing them.
package detect

import (
	"bufio"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	return Result{Path: name, Kind: "unknown", Profile: "unknown", Evidence: []string{"no recognized executable signature"}}, nil
}

func directory(name string) (Result, error) {
	if _, err := os.ReadDir(name); err != nil {
		return Result{}, fmt.Errorf("read project directory: %w", err)
	}
	return Result{Path: name, Kind: "unknown", Profile: "unknown", Evidence: []string{"directory language classification is delegated to language plugins"}}, nil
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
	return Result{Path: name, Kind: "script", Profile: "unknown", Interpreter: shebang, Evidence: []string{"shebang"}}, true, nil
}

func JSON(result Result) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}

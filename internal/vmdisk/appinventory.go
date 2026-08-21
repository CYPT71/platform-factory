package vmdisk

import (
	"bytes"
	"debug/elf"
	"fmt"
	"path"
	"sort"
	"strings"
)

type ApplicationInventory struct {
	Executables      []ApplicationFinding `json:"executables"`
	Interpreters     []ApplicationFinding `json:"interpreters"`
	Runtimes         []ApplicationFinding `json:"runtimes"`
	SharedLibraries  []ApplicationFinding `json:"shared_libraries"`
	Configuration    []ApplicationFinding `json:"configuration_files"`
	Certificates     []ApplicationFinding `json:"certificates"`
	SecretCandidates []ApplicationFinding `json:"secret_candidates"`
	UnixSockets      []ApplicationFinding `json:"unix_sockets"`
	Devices          []ApplicationFinding `json:"devices"`
	KernelModules    []ApplicationFinding `json:"kernel_modules"`
	ELFDependencies  []ApplicationFinding `json:"elf_dependencies"`
	SpecialPaths     []ApplicationFinding `json:"special_filesystem_dependencies"`
	MainProcess      *ApplicationFinding  `json:"main_process,omitempty"`
}

type ApplicationFinding struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Confidence string `json:"confidence"`
	Evidence   string `json:"evidence"`
}

func DetectApplications(filesystem FilesystemInventory) ApplicationInventory {
	result := ApplicationInventory{Executables: []ApplicationFinding{}, Interpreters: []ApplicationFinding{}, Runtimes: []ApplicationFinding{}, SharedLibraries: []ApplicationFinding{}, Configuration: []ApplicationFinding{}, Certificates: []ApplicationFinding{}, SecretCandidates: []ApplicationFinding{}, UnixSockets: []ApplicationFinding{}, Devices: []ApplicationFinding{}, KernelModules: []ApplicationFinding{}, ELFDependencies: []ApplicationFinding{}, SpecialPaths: []ApplicationFinding{}}
	interpreterNames := map[string]bool{"sh": true, "bash": true, "dash": true, "busybox": true, "python": true, "python3": true, "node": true, "ruby": true, "php": true, "perl": true}
	runtimeNames := map[string]bool{"python": true, "python3": true, "node": true, "java": true, "dotnet": true, "ruby": true, "php": true, "busybox": true}
	secretNames := []string{".env", "id_rsa", "id_ed25519", "credentials", "credential", "secret", "token", "apikey", "api_key"}
	for _, file := range filesystem.Files {
		base := strings.ToLower(path.Base(file.Path))
		lowerPath := strings.ToLower(file.Path)
		if file.Type == "file" && file.Mode&0o111 != 0 {
			result.Executables = append(result.Executables, finding(file.Path, "executable", "high", "regular file has POSIX execute bits"))
			if interpreterNames[base] {
				result.Interpreters = append(result.Interpreters, finding(file.Path, "interpreter", "high", "known interpreter basename and executable mode"))
			}
			if runtimeNames[base] {
				result.Runtimes = append(result.Runtimes, finding(file.Path, "runtime", "high", "known runtime basename and executable mode"))
			}
		}
		if file.Type == "file" && strings.Contains(lowerPath, "/lib") && (strings.Contains(base, ".so.") || strings.HasSuffix(base, ".so")) {
			result.SharedLibraries = append(result.SharedLibraries, finding(file.Path, "shared_library", "high", "library path and .so filename"))
		}
		if file.Type == "file" && (strings.HasPrefix(file.Path, "/etc/") || strings.HasSuffix(base, ".conf") || strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".toml") || strings.HasSuffix(base, ".ini")) {
			result.Configuration = append(result.Configuration, finding(file.Path, "configuration", "medium", "configuration path or extension"))
		}
		if file.Type == "file" && (strings.HasSuffix(base, ".crt") || strings.HasSuffix(base, ".cer") || strings.HasSuffix(base, ".pem")) {
			result.Certificates = append(result.Certificates, finding(file.Path, "certificate_candidate", "medium", "certificate filename extension; content not read"))
		}
		if file.Type == "file" {
			for _, marker := range secretNames {
				if base == marker || strings.Contains(base, marker) {
					result.SecretCandidates = append(result.SecretCandidates, finding(file.Path, "probable_secret", "medium", "sensitive filename marker; content deliberately not read"))
					break
				}
			}
		}
		switch file.Type {
		case "socket":
			result.UnixSockets = append(result.UnixSockets, finding(file.Path, "unix_socket", "high", "ext inode type is socket"))
		case "character_device", "block_device":
			result.Devices = append(result.Devices, finding(file.Path, file.Type, "high", "ext inode type is device"))
		}
		if file.Type == "file" && strings.HasPrefix(file.Path, "/lib/modules/") && (strings.HasSuffix(base, ".ko") || strings.Contains(base, ".ko.")) {
			result.KernelModules = append(result.KernelModules, finding(file.Path, "kernel_module", "high", "module tree path and .ko filename"))
		}
	}
	sortApplicationInventory(&result)
	return result
}

// AnalyzeExtApplicationContent enriches metadata findings from bounded,
// read-only file contents. At most 256 files and 16 MiB are inspected, and no
// content is copied into the report.
func AnalyzeExtApplicationContent(diskPath string, format Format, volume Volume, filesystem FilesystemInventory, result ApplicationInventory) (ApplicationInventory, error) {
	const maxFiles, maxBytes = 256, 16 << 20
	inspected, total := 0, 0
	seenDependencies, seenSpecial := map[string]bool{}, map[string]bool{}
	for _, file := range filesystem.Files {
		candidate := file.Type == "file" && (file.Mode&0o111 != 0 || strings.HasPrefix(file.Path, "/etc/") || strings.HasSuffix(file.Path, ".service"))
		if !candidate || inspected >= maxFiles || total >= maxBytes {
			continue
		}
		content, err := ReadExtFile(diskPath, format, volume, file.Path)
		if err != nil {
			return result, fmt.Errorf("analyze application file %s: %w", file.Path, err)
		}
		inspected++
		total += len(content)
		if total > maxBytes {
			break
		}
		if file.Mode&0o111 != 0 {
			for _, library := range importedELFLibraries(content) {
				key := file.Path + "\x00" + library
				if !seenDependencies[key] {
					seenDependencies[key] = true
					result.ELFDependencies = append(result.ELFDependencies, finding(library, "elf_dependency", "high", "DT_NEEDED entry required by "+file.Path))
				}
			}
		}
		for _, special := range []string{"/proc", "/sys", "/dev"} {
			if containsAbsolutePathReference(content, special) && !seenSpecial[special] {
				seenSpecial[special] = true
				result.SpecialPaths = append(result.SpecialPaths, finding(special, "special_filesystem_dependency", "medium", "absolute path reference found in "+file.Path))
			}
		}
		if result.MainProcess == nil && strings.HasSuffix(file.Path, ".service") {
			if executable := parseSystemdExecStart(content); executable != "" {
				finding := finding(executable, "main_process", "high", "ExecStart in "+file.Path)
				result.MainProcess = &finding
			}
		}
	}
	sortApplicationInventory(&result)
	return result, nil
}

func importedELFLibraries(content []byte) []string {
	f, err := elf.NewFile(bytes.NewReader(content))
	if err != nil {
		return nil
	}
	defer f.Close()
	libraries, err := f.ImportedLibraries()
	if err != nil {
		return nil
	}
	sort.Strings(libraries)
	return libraries
}

func containsAbsolutePathReference(content []byte, root string) bool {
	for _, suffix := range []string{"/", "\x00", " ", "\n", "\t", "\"", "'"} {
		if bytes.Contains(content, []byte(root+suffix)) {
			return true
		}
	}
	return false
}

func parseSystemdExecStart(content []byte) string {
	if len(content) > 1<<20 || bytes.IndexByte(content, 0) >= 0 {
		return ""
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		command := strings.TrimSpace(strings.TrimPrefix(line, "ExecStart="))
		command = strings.TrimLeft(command, "-+!:@")
		if fields := strings.Fields(command); len(fields) > 0 && strings.HasPrefix(fields[0], "/") && path.Clean(fields[0]) == fields[0] {
			return fields[0]
		}
	}
	return ""
}

func finding(pathname, kind, confidence, evidence string) ApplicationFinding {
	return ApplicationFinding{Path: pathname, Kind: kind, Confidence: confidence, Evidence: evidence}
}

func sortApplicationInventory(result *ApplicationInventory) {
	collections := []*[]ApplicationFinding{&result.Executables, &result.Interpreters, &result.Runtimes, &result.SharedLibraries, &result.Configuration, &result.Certificates, &result.SecretCandidates, &result.UnixSockets, &result.Devices, &result.KernelModules, &result.ELFDependencies, &result.SpecialPaths}
	for _, collection := range collections {
		sort.Slice(*collection, func(i, j int) bool { return (*collection)[i].Path < (*collection)[j].Path })
	}
}

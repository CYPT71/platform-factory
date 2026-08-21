package microvm

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/CYPT71/platform-factory/internal/detect"
	"github.com/CYPT71/platform-factory/internal/vmdisk"
	"github.com/CYPT71/platform-factory/oci"
)

type LegacyOCIOptions struct {
	ExtractionRoot string
	Report         vmdisk.ExtractionReport
	Entrypoint     string
	Output         string
	Architecture   string
	Image          string
	Tag            string
	RuntimeUser    string
}

type LegacyOCIResult struct {
	Digest      string            `json:"digest"`
	Output      string            `json:"output"`
	Entrypoint  string            `json:"entrypoint"`
	Profile     string            `json:"profile"`
	User        string            `json:"user"`
	WorkingDir  string            `json:"working_dir"`
	Volumes     []string          `json:"volumes"`
	Ports       []string          `json:"ports"`
	Args        []string          `json:"args"`
	Environment map[string]string `json:"environment"`
}

// BuildLegacyOCI turns a reviewed extraction into a deterministic OCI layout.
// It preserves source file mode/ownership in tar headers, derives the runtime
// user and working directory from the selected main executable, and relies on
// oci.Build's ELF closure validator before installing any layout.
func BuildLegacyOCI(options LegacyOCIOptions) (LegacyOCIResult, error) {
	if !options.Report.Complete && !options.Report.ApprovedIncomplete {
		return LegacyOCIResult{}, vmdisk.ErrIncompleteExtraction
	}
	if options.Entrypoint == "" || !strings.HasPrefix(options.Entrypoint, "/") || path.Clean(options.Entrypoint) != options.Entrypoint || options.Entrypoint == "/" {
		return LegacyOCIResult{}, fmt.Errorf("legacy OCI entrypoint must be an absolute clean path")
	}
	var main *vmdisk.ExtractedFile
	for index := range options.Report.Extracted {
		if options.Report.Extracted[index].Path == options.Entrypoint {
			main = &options.Report.Extracted[index]
			break
		}
	}
	if main == nil || main.Mode&0o111 == 0 {
		return LegacyOCIResult{}, fmt.Errorf("legacy OCI entrypoint must be an extracted executable")
	}
	root, err := filepath.Abs(options.ExtractionRoot)
	if err != nil {
		return LegacyOCIResult{}, err
	}
	sourceFor := func(containerPath string) (string, error) {
		candidate := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(containerPath, "/")))
		relative, err := filepath.Rel(root, candidate)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("legacy OCI source escapes extraction root")
		}
		info, err := os.Lstat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("legacy OCI source %s is not a regular non-symlink file", containerPath)
		}
		return candidate, nil
	}
	binary, err := sourceFor(main.Path)
	if err != nil {
		return LegacyOCIResult{}, err
	}
	detection, err := detect.Path(binary)
	if err != nil {
		return LegacyOCIResult{}, err
	}
	if detection.Kind != "elf" || (detection.Profile != "static" && detection.Profile != "glibc" && detection.Profile != "musl") {
		return LegacyOCIResult{}, fmt.Errorf("legacy OCI entrypoint must be a supported Linux ELF; detected %s/%s", detection.Kind, detection.Profile)
	}
	architecture := options.Architecture
	if architecture == "" {
		architecture = detection.Architecture
	}
	if detection.Architecture != architecture {
		return LegacyOCIResult{}, fmt.Errorf("legacy entrypoint architecture %s does not match %s", detection.Architecture, architecture)
	}
	var service *vmdisk.ServiceConfiguration
	if options.Report.System != nil {
		for index := range options.Report.System.ServiceConfigurations {
			candidate := &options.Report.System.ServiceConfigurations[index]
			if candidate.Entrypoint != main.Path {
				continue
			}
			if service != nil {
				return LegacyOCIResult{}, fmt.Errorf("multiple systemd services select legacy entrypoint %s", main.Path)
			}
			service = candidate
		}
	}
	if service != nil && (len(service.SecretEnvironmentKeys) > 0 || len(service.IncompleteReasons) > 0) && !options.Report.ApprovedIncomplete {
		return LegacyOCIResult{}, fmt.Errorf("%w: matching systemd service has omitted or unsupported runtime fields", vmdisk.ErrIncompleteExtraction)
	}
	user := options.RuntimeUser
	if user == "" && service != nil && service.User != "" {
		resolvedUID, resolvedGID, err := resolveLegacyServiceIdentity(*options.Report.System, service.User, service.Group)
		if err != nil {
			return LegacyOCIResult{}, err
		}
		user = strconv.FormatUint(uint64(resolvedUID), 10) + ":" + strconv.FormatUint(uint64(resolvedGID), 10)
	}
	if user == "" {
		if main.UID == 0 || main.GID == 0 {
			return LegacyOCIResult{}, fmt.Errorf("legacy entrypoint is root-owned; provide an explicit positive --user mapping")
		}
		user = strconv.FormatUint(uint64(main.UID), 10) + ":" + strconv.FormatUint(uint64(main.GID), 10)
	}
	extras := make([]oci.ExtraFile, 0, len(options.Report.Extracted)-1)
	for _, extracted := range options.Report.Extracted {
		if extracted.Path == main.Path {
			continue
		}
		source, err := sourceFor(extracted.Path)
		if err != nil {
			return LegacyOCIResult{}, err
		}
		extras = append(extras, oci.ExtraFile{Dest: extracted.Path, Source: source, Mode: int64(extracted.Mode), UID: extracted.UID, GID: extracted.GID, PreserveOwnership: true, Category: oci.CategoryApplication})
	}
	workingDirectory := path.Dir(main.Path)
	if workingDirectory == "/" {
		workingDirectory = ""
	}
	args, environment := []string{}, map[string]string{}
	if service != nil {
		args = append(args, service.Args...)
		for key, value := range service.Environment {
			environment[key] = value
		}
		if service.WorkingDirectory != "" {
			workingDirectory = service.WorkingDirectory
		}
	}
	volumes, ports := []string{}, []string{}
	if options.Report.System != nil {
		seenVolumes, seenPorts := map[string]bool{}, map[string]bool{}
		for _, mount := range options.Report.System.Mounts {
			if !seenVolumes[mount.MountPoint] {
				seenVolumes[mount.MountPoint] = true
				volumes = append(volumes, mount.MountPoint)
			}
		}
		for _, port := range options.Report.System.ProbablePorts {
			value := strconv.Itoa(port) + "/tcp"
			if !seenPorts[value] {
				seenPorts[value] = true
				ports = append(ports, value)
			}
		}
		sort.Strings(volumes)
		sort.Strings(ports)
	}
	digest, err := oci.Build(oci.Options{Binary: binary, Output: options.Output, Architecture: architecture, OS: "linux", Entrypoint: main.Path, Profile: detection.Profile, ImageName: options.Image, Tag: options.Tag, BinaryMode: int64(main.Mode), BinaryUID: main.UID, BinaryGID: main.GID, PreserveBinaryOwnership: true, ExtraFiles: extras, WorkingDir: workingDirectory, User: user, Home: "/home/nonroot", IdentityFiles: true, SemanticLayers: true, Volumes: volumes, Ports: ports, Args: args, Env: environment})
	if err != nil {
		return LegacyOCIResult{}, err
	}
	return LegacyOCIResult{Digest: digest, Output: options.Output, Entrypoint: main.Path, Profile: detection.Profile, User: user, WorkingDir: workingDirectory, Volumes: volumes, Ports: ports, Args: args, Environment: environment}, nil
}

func resolveLegacyServiceIdentity(system vmdisk.SystemInventory, userValue, groupValue string) (uint32, uint32, error) {
	parseID := func(value string) (uint32, bool) {
		parsed, err := strconv.ParseUint(value, 10, 32)
		return uint32(parsed), err == nil
	}
	uid, userNumeric := parseID(userValue)
	var primaryGID uint32
	if !userNumeric {
		found := false
		for _, user := range system.Users {
			if user.Name == userValue {
				uid, primaryGID, found = user.UID, user.GID, true
				break
			}
		}
		if !found {
			return 0, 0, fmt.Errorf("systemd User %q is absent from validated passwd inventory", userValue)
		}
	}
	gid := primaryGID
	if groupValue != "" {
		if parsed, numeric := parseID(groupValue); numeric {
			gid = parsed
		} else {
			found := false
			for _, group := range system.Groups {
				if group.Name == groupValue {
					gid, found = group.GID, true
					break
				}
			}
			if !found {
				return 0, 0, fmt.Errorf("systemd Group %q is absent from validated group inventory", groupValue)
			}
		}
	} else if userNumeric {
		gid = uid
	}
	if uid == 0 || gid == 0 {
		return 0, 0, fmt.Errorf("systemd service resolves to root; provide an explicit positive --user mapping")
	}
	return uid, gid, nil
}

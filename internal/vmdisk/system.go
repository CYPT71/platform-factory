package vmdisk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

type SystemInventory struct {
	Distribution                string                 `json:"distribution"`
	Version                     string                 `json:"version"`
	Architecture                string                 `json:"architecture"`
	Kernel                      string                 `json:"kernel"`
	InitSystems                 []string               `json:"init_systems"`
	Services                    []string               `json:"services"`
	StartupFiles                []string               `json:"startup_files"`
	CronFiles                   []string               `json:"cron_files"`
	SystemdTimers               []string               `json:"systemd_timers"`
	Users                       []SystemUser           `json:"users"`
	Groups                      []SystemGroup          `json:"groups"`
	Mounts                      []SystemMount          `json:"mounts"`
	NetworkServices             []NetworkService       `json:"network_services"`
	ProbablePorts               []int                  `json:"probable_ports"`
	Facts                       []DetectionFact        `json:"facts"`
	ServiceConfigurations       []ServiceConfiguration `json:"service_configurations"`
	ServiceInspectionIncomplete bool                   `json:"service_inspection_incomplete"`
}

type ServiceConfiguration struct {
	Source                string            `json:"source"`
	Entrypoint            string            `json:"entrypoint"`
	Args                  []string          `json:"args"`
	Environment           map[string]string `json:"environment"`
	SecretEnvironmentKeys []string          `json:"secret_environment_keys"`
	WorkingDirectory      string            `json:"working_directory,omitempty"`
	User                  string            `json:"user,omitempty"`
	Group                 string            `json:"group,omitempty"`
	IncompleteReasons     []string          `json:"incomplete_reasons"`
}

type SystemUser struct {
	Name  string `json:"name"`
	UID   uint32 `json:"uid"`
	GID   uint32 `json:"gid"`
	Home  string `json:"home"`
	Shell string `json:"shell"`
}

type SystemGroup struct {
	Name    string   `json:"name"`
	GID     uint32   `json:"gid"`
	Members []string `json:"members"`
}

type SystemMount struct {
	Source     string   `json:"source"`
	MountPoint string   `json:"mount_point"`
	Filesystem string   `json:"filesystem"`
	Options    []string `json:"options"`
}

type NetworkService struct {
	Name       string `json:"name"`
	ConfigPath string `json:"config_path"`
	Ports      []int  `json:"ports"`
	Confidence string `json:"confidence"`
}

type DetectionFact struct {
	Kind       string `json:"kind"`
	Value      string `json:"value"`
	Confidence string `json:"confidence"`
	Evidence   string `json:"evidence"`
}

func DetectLinuxSystem(diskPath string, format Format, volume Volume, filesystem FilesystemInventory) (SystemInventory, error) {
	result := SystemInventory{InitSystems: []string{}, Services: []string{}, StartupFiles: []string{}, CronFiles: []string{}, SystemdTimers: []string{}, Users: []SystemUser{}, Groups: []SystemGroup{}, Mounts: []SystemMount{}, NetworkServices: []NetworkService{}, ProbablePorts: []int{}, Facts: []DetectionFact{}, ServiceConfigurations: []ServiceConfiguration{}}
	paths := make(map[string]InventoryFile, len(filesystem.Files))
	for _, file := range filesystem.Files {
		paths[file.Path] = file
	}
	osReleasePath := ""
	for _, candidate := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if file, ok := paths[candidate]; ok && file.Type == "file" {
			osReleasePath = candidate
			break
		}
	}
	if osReleasePath != "" {
		content, err := ReadExtFile(diskPath, format, volume, osReleasePath)
		if err != nil {
			return SystemInventory{}, fmt.Errorf("vmdisk: read os-release: %w", err)
		}
		values, err := parseOSRelease(content)
		if err != nil {
			return SystemInventory{}, err
		}
		result.Distribution = values["ID"]
		if result.Distribution == "" {
			result.Distribution = values["NAME"]
		}
		result.Version = values["VERSION_ID"]
		if result.Version == "" {
			result.Version = values["VERSION"]
		}
		if result.Distribution != "" {
			result.Facts = append(result.Facts, DetectionFact{"distribution", result.Distribution, "high", osReleasePath + " ID/NAME"})
		}
		if result.Version != "" {
			result.Facts = append(result.Facts, DetectionFact{"version", result.Version, "high", osReleasePath + " VERSION_ID/VERSION"})
		}
	}
	for _, candidate := range []string{"/bin/sh", "/bin/busybox", "/usr/bin/env", "/sbin/init"} {
		if file, ok := paths[candidate]; !ok || file.Type != "file" {
			continue
		}
		content, err := ReadExtFile(diskPath, format, volume, candidate)
		if err != nil {
			return SystemInventory{}, fmt.Errorf("vmdisk: read ELF candidate %s: %w", candidate, err)
		}
		if architecture := elfArchitecture(content); architecture != "" {
			result.Architecture = architecture
			result.Facts = append(result.Facts, DetectionFact{"architecture", architecture, "high", candidate + " ELF e_machine"})
			break
		}
	}
	for pathname := range paths {
		if strings.HasPrefix(pathname, "/boot/vmlinuz-") || strings.HasPrefix(pathname, "/boot/kernel-") {
			result.Kernel = strings.TrimPrefix(strings.TrimPrefix(pathname, "/boot/vmlinuz-"), "/boot/kernel-")
			result.Facts = append(result.Facts, DetectionFact{"kernel", result.Kernel, "medium", pathname + " filename"})
			break
		}
	}
	detectInitAndStartup(paths, &result)
	sort.Strings(result.Services)
	const maxServiceUnits, maxServiceBytes = 64, uint64(1 << 20)
	var serviceBytes uint64
	for _, servicePath := range result.Services {
		file := paths[servicePath]
		if len(result.ServiceConfigurations) >= maxServiceUnits || file.Size > maxSemanticFileBytes || serviceBytes > maxServiceBytes-file.Size {
			result.ServiceInspectionIncomplete = true
			continue
		}
		content, err := ReadExtFile(diskPath, format, volume, servicePath)
		if err != nil {
			return SystemInventory{}, fmt.Errorf("vmdisk: read systemd unit %s: %w", servicePath, err)
		}
		serviceBytes += uint64(len(content))
		configuration, err := parseSystemdServiceConfiguration(servicePath, content)
		if err != nil {
			return SystemInventory{}, err
		}
		result.ServiceConfigurations = append(result.ServiceConfigurations, configuration)
	}
	if file, ok := paths["/etc/passwd"]; ok && file.Type == "file" {
		content, err := ReadExtFile(diskPath, format, volume, "/etc/passwd")
		if err != nil {
			return SystemInventory{}, err
		}
		result.Users, err = parsePasswd(content)
		if err != nil {
			return SystemInventory{}, err
		}
		result.Facts = append(result.Facts, DetectionFact{"users", strconv.Itoa(len(result.Users)), "high", "/etc/passwd validated entries"})
	}
	if file, ok := paths["/etc/group"]; ok && file.Type == "file" {
		content, err := ReadExtFile(diskPath, format, volume, "/etc/group")
		if err != nil {
			return SystemInventory{}, err
		}
		result.Groups, err = parseGroup(content)
		if err != nil {
			return SystemInventory{}, err
		}
		result.Facts = append(result.Facts, DetectionFact{"groups", strconv.Itoa(len(result.Groups)), "high", "/etc/group validated entries"})
	}
	if file, ok := paths["/etc/fstab"]; ok && file.Type == "file" {
		content, err := ReadExtFile(diskPath, format, volume, "/etc/fstab")
		if err != nil {
			return SystemInventory{}, err
		}
		result.Mounts, err = parseFSTab(content)
		if err != nil {
			return SystemInventory{}, err
		}
		result.Facts = append(result.Facts, DetectionFact{"persistent_mounts", strconv.Itoa(len(result.Mounts)), "high", "/etc/fstab validated persistent entries"})
	}
	if file, ok := paths["/etc/ssh/sshd_config"]; ok && file.Type == "file" {
		content, err := ReadExtFile(diskPath, format, volume, "/etc/ssh/sshd_config")
		if err != nil {
			return SystemInventory{}, err
		}
		ports, err := parseSSHPorts(content)
		if err != nil {
			return SystemInventory{}, err
		}
		result.NetworkServices = append(result.NetworkServices, NetworkService{Name: "sshd", ConfigPath: "/etc/ssh/sshd_config", Ports: ports, Confidence: "high"})
		result.ProbablePorts = append(result.ProbablePorts, ports...)
		result.Facts = append(result.Facts, DetectionFact{"network_service", "sshd", "high", "/etc/ssh/sshd_config validated"})
	}
	sort.Strings(result.InitSystems)
	sort.Strings(result.Services)
	sort.Strings(result.StartupFiles)
	sort.Strings(result.CronFiles)
	sort.Strings(result.SystemdTimers)
	sort.Ints(result.ProbablePorts)
	sort.Slice(result.ServiceConfigurations, func(i, j int) bool {
		return result.ServiceConfigurations[i].Source < result.ServiceConfigurations[j].Source
	})
	return result, nil
}

func parseSystemdServiceConfiguration(source string, content []byte) (ServiceConfiguration, error) {
	result := ServiceConfiguration{Source: source, Args: []string{}, Environment: map[string]string{}, SecretEnvironmentKeys: []string{}, IncompleteReasons: []string{}}
	if len(content) > maxSemanticFileBytes || bytes.IndexByte(content, 0) >= 0 {
		return result, fmt.Errorf("%w: systemd unit is oversized or contains NUL", ErrCorruptFilesystem)
	}
	inService, execSeen := false, false
	secretKeys := map[string]bool{}
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return result, fmt.Errorf("%w: malformed systemd section", ErrCorruptFilesystem)
			}
			inService = line == "[Service]"
			continue
		}
		if !inService {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return result, fmt.Errorf("%w: malformed systemd directive", ErrCorruptFilesystem)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "ExecStart":
			if execSeen {
				result.IncompleteReasons = append(result.IncompleteReasons, "multiple ExecStart directives require manual review")
				continue
			}
			words, err := splitSystemdWords(strings.TrimLeft(value, "-+!:@"))
			if err != nil || len(words) == 0 || !strings.HasPrefix(words[0], "/") || path.Clean(words[0]) != words[0] || strings.ContainsAny(value, "$%") {
				result.IncompleteReasons = append(result.IncompleteReasons, "ExecStart uses unsupported expansion or quoting")
				continue
			}
			result.Entrypoint, result.Args, execSeen = words[0], append([]string(nil), words[1:]...), true
		case "Environment":
			assignments, err := splitSystemdWords(value)
			if err != nil {
				result.IncompleteReasons = append(result.IncompleteReasons, "Environment uses unsupported quoting")
				continue
			}
			for _, assignment := range assignments {
				name, environmentValue, ok := strings.Cut(assignment, "=")
				if !ok || name == "" || strings.Trim(name, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_") != "" || strings.ContainsRune(name, 0) {
					return result, fmt.Errorf("%w: malformed systemd environment assignment", ErrCorruptFilesystem)
				}
				if secretEnvironmentName(name) {
					secretKeys[name] = true
					continue
				}
				result.Environment[name] = environmentValue
			}
		case "EnvironmentFile":
			result.IncompleteReasons = append(result.IncompleteReasons, "EnvironmentFile values are not imported automatically")
		case "WorkingDirectory":
			if !strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "/" {
				result.IncompleteReasons = append(result.IncompleteReasons, "WorkingDirectory is not an absolute clean non-root path")
			} else {
				result.WorkingDirectory = value
			}
		case "User":
			if !safeSystemdIdentity(value) {
				return result, fmt.Errorf("%w: unsafe systemd User", ErrCorruptFilesystem)
			}
			result.User = value
		case "Group":
			if !safeSystemdIdentity(value) {
				return result, fmt.Errorf("%w: unsafe systemd Group", ErrCorruptFilesystem)
			}
			result.Group = value
		}
	}
	if !execSeen {
		result.IncompleteReasons = append(result.IncompleteReasons, "no supported ExecStart directive")
	}
	for key := range secretKeys {
		result.SecretEnvironmentKeys = append(result.SecretEnvironmentKeys, key)
	}
	sort.Strings(result.SecretEnvironmentKeys)
	sort.Strings(result.IncompleteReasons)
	return result, nil
}

func splitSystemdWords(value string) ([]string, error) {
	words, current := []string{}, strings.Builder{}
	var quote rune
	escaped, started := false, false
	flush := func() {
		if started {
			words = append(words, current.String())
			current.Reset()
			started = false
		}
	}
	for _, character := range value {
		if escaped {
			current.WriteRune(character)
			escaped, started = false, true
			continue
		}
		if character == '\\' {
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			started = true
			continue
		}
		if character == '\'' || character == '"' {
			quote, started = character, true
			continue
		}
		if character == ' ' || character == '\t' {
			flush()
			continue
		}
		current.WriteRune(character)
		started = true
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated systemd word")
	}
	flush()
	return words, nil
}

func secretEnvironmentName(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"PASSWORD", "PASSWD", "SECRET", "TOKEN", "API_KEY", "APIKEY", "CREDENTIAL", "PRIVATE_KEY"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func safeSystemdIdentity(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	return strings.Trim(value, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-") == ""
}

func parseSSHPorts(content []byte) ([]int, error) {
	ports := []int{}
	seen := map[int]bool{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.EqualFold(fields[0], "Port") {
			if len(fields) != 2 {
				return nil, fmt.Errorf("%w: malformed ssh Port directive", ErrCorruptFilesystem)
			}
			port, err := strconv.Atoi(fields[1])
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("%w: invalid ssh port", ErrCorruptFilesystem)
			}
			if !seen[port] {
				ports = append(ports, port)
				seen[port] = true
			}
		}
	}
	if len(ports) == 0 {
		ports = append(ports, 22)
	}
	sort.Ints(ports)
	return ports, nil
}

func parsePasswd(content []byte) ([]SystemUser, error) {
	var users []SystemUser
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 7 || !safeAccountName(fields[0]) {
			return nil, fmt.Errorf("%w: malformed passwd entry", ErrCorruptFilesystem)
		}
		uid, uidErr := strconv.ParseUint(fields[2], 10, 32)
		gid, gidErr := strconv.ParseUint(fields[3], 10, 32)
		if uidErr != nil || gidErr != nil || !safeAccountPath(fields[5]) || !safeAccountPath(fields[6]) {
			return nil, fmt.Errorf("%w: malformed passwd identity", ErrCorruptFilesystem)
		}
		users = append(users, SystemUser{Name: fields[0], UID: uint32(uid), GID: uint32(gid), Home: fields[5], Shell: fields[6]})
	}
	return users, nil
}

func parseGroup(content []byte) ([]SystemGroup, error) {
	var groups []SystemGroup
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 4 || !safeAccountName(fields[0]) {
			return nil, fmt.Errorf("%w: malformed group entry", ErrCorruptFilesystem)
		}
		gid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed group identity", ErrCorruptFilesystem)
		}
		members := []string{}
		if fields[3] != "" {
			members = strings.Split(fields[3], ",")
			for _, member := range members {
				if !safeAccountName(member) {
					return nil, fmt.Errorf("%w: malformed group member", ErrCorruptFilesystem)
				}
			}
		}
		groups = append(groups, SystemGroup{Name: fields[0], GID: uint32(gid), Members: members})
	}
	return groups, nil
}

func safeAccountName(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, ":,\r\n\x00")
}

func safeAccountPath(value string) bool {
	return len(value) <= 4096 && !strings.ContainsAny(value, ":\r\n\x00")
}

func parseFSTab(content []byte) ([]SystemMount, error) {
	pseudo := map[string]bool{"proc": true, "sysfs": true, "tmpfs": true, "devtmpfs": true, "devpts": true, "cgroup": true, "cgroup2": true, "swap": true}
	mounts := []SystemMount{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 || len(fields) > 6 || strings.ContainsAny(strings.Join(fields[:4], ""), "\x00\r") {
			return nil, fmt.Errorf("%w: malformed fstab entry", ErrCorruptFilesystem)
		}
		if pseudo[fields[2]] {
			continue
		}
		if !strings.HasPrefix(fields[1], "/") || path.Clean(fields[1]) != fields[1] {
			return nil, fmt.Errorf("%w: malformed persistent mount path", ErrCorruptFilesystem)
		}
		mounts = append(mounts, SystemMount{Source: fields[0], MountPoint: fields[1], Filesystem: fields[2], Options: strings.Split(fields[3], ",")})
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].MountPoint < mounts[j].MountPoint })
	return mounts, nil
}

func parseOSRelease(content []byte) (map[string]string, error) {
	if len(content) > maxSemanticFileBytes || bytes.IndexByte(content, 0) >= 0 {
		return nil, fmt.Errorf("%w: os-release is oversized or contains NUL", ErrCorruptFilesystem)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || strings.Trim(key, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_") != "" {
			return nil, fmt.Errorf("%w: malformed os-release assignment", ErrCorruptFilesystem)
		}
		if strings.HasPrefix(value, `"`) || strings.HasPrefix(value, `'`) {
			if strings.HasPrefix(value, `'`) {
				if len(value) < 2 || value[len(value)-1] != '\'' {
					return nil, fmt.Errorf("%w: malformed os-release quote", ErrCorruptFilesystem)
				}
				value = value[1 : len(value)-1]
			} else {
				decoded, err := strconv.Unquote(value)
				if err != nil {
					return nil, fmt.Errorf("%w: malformed os-release quote", ErrCorruptFilesystem)
				}
				value = decoded
			}
		}
		if len(value) > 4096 || strings.ContainsAny(value, "\r\x00") {
			return nil, fmt.Errorf("%w: unsafe os-release value", ErrCorruptFilesystem)
		}
		values[key] = value
	}
	return values, nil
}

func elfArchitecture(content []byte) string {
	if len(content) < 20 || !bytes.Equal(content[:4], []byte{0x7f, 'E', 'L', 'F'}) {
		return ""
	}
	var machine uint16
	switch content[5] {
	case 1:
		machine = binary.LittleEndian.Uint16(content[18:20])
	case 2:
		machine = binary.BigEndian.Uint16(content[18:20])
	default:
		return ""
	}
	return map[uint16]string{3: "386", 40: "arm", 62: "amd64", 183: "arm64", 243: "riscv64"}[machine]
}

func detectInitAndStartup(paths map[string]InventoryFile, result *SystemInventory) {
	initSet := map[string]bool{}
	for pathname, file := range paths {
		switch {
		case strings.Contains(pathname, "/systemd/system/") && strings.HasSuffix(pathname, ".service"):
			initSet["systemd"] = true
			result.Services = append(result.Services, pathname)
		case strings.Contains(pathname, "/systemd/system/") && strings.HasSuffix(pathname, ".timer"):
			initSet["systemd"] = true
			result.SystemdTimers = append(result.SystemdTimers, pathname)
		case strings.HasPrefix(pathname, "/etc/init.d/") && file.Type == "file":
			initSet["sysv"] = true
			result.StartupFiles = append(result.StartupFiles, pathname)
		case strings.HasPrefix(pathname, "/etc/runlevels/"):
			initSet["openrc"] = true
		case pathname == "/bin/busybox" || pathname == "/sbin/busybox":
			initSet["busybox"] = true
		}
		if pathname == "/etc/crontab" || strings.HasPrefix(pathname, "/etc/cron.") || strings.HasPrefix(pathname, "/var/spool/cron/") {
			result.CronFiles = append(result.CronFiles, pathname)
		}
	}
	for name := range initSet {
		result.InitSystems = append(result.InitSystems, name)
		result.Facts = append(result.Facts, DetectionFact{"init", name, "high", "filesystem path evidence"})
	}
}

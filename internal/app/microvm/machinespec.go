package microvm

import (
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	api "github.com/CYPT71/platform-factory/internal/microvm"
	"github.com/CYPT71/platform-factory/internal/networking"
	vmruntime "github.com/CYPT71/platform-factory/internal/runtime"
)

type VolumeMapping struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type MachineSpecOptions struct {
	ID              string
	KernelDigest    string
	InitramfsDigest string
	ManifestDigest  string
	RootFSDigest    string
	CommandLine     []string
	MemoryMiB       uint64
	VCPUs           uint32
	RequiredPorts   []string
	RequiredVolumes []string
	Forwards        []networking.Forward
	Volumes         []VolumeMapping
	DNS             []string
}

// BuildMachineSpec turns verified packager requirements into the native VMM
// contract. Required OCI volumes never acquire guessed host sources and every
// exposed port must have an explicit matching forward.
func BuildMachineSpec(options MachineSpecOptions) (api.MachineSpec, error) {
	if !api.NamePattern.MatchString(options.ID) {
		return api.MachineSpec{}, fmt.Errorf("machine id must be a DNS label of at most 63 characters")
	}
	if options.VCPUs == 0 || options.VCPUs > api.MaxVCPUs || options.MemoryMiB < api.MinMemoryMiB || options.MemoryMiB > api.MaxMemoryMiB {
		return api.MachineSpec{}, fmt.Errorf("machine resources are outside supported limits")
	}
	if !validSHA256(options.ManifestDigest) {
		return api.MachineSpec{}, fmt.Errorf("OCI manifest must be pinned by sha256 digest")
	}
	bundle, err := vmruntime.NewBootBundle(options.KernelDigest, options.InitramfsDigest, options.RootFSDigest, options.CommandLine, map[string]string{
		"platform-factory.dev/oci-manifest":  options.ManifestDigest,
		"platform-factory.dev/rootfs-format": "initramfs",
	})
	if err != nil {
		return api.MachineSpec{}, err
	}
	ports := make([]api.Port, 0, len(options.Forwards))
	coveredPorts := map[string]bool{}
	for _, forward := range options.Forwards {
		if forward.Protocol != "tcp" && forward.Protocol != "udp" || forward.HostPort < 1 || forward.HostPort > 65535 || forward.GuestPort < 1 || forward.GuestPort > 65535 {
			return api.MachineSpec{}, fmt.Errorf("invalid machine port forward")
		}
		hostIP := forward.HostIP
		if hostIP == "" {
			hostIP = "127.0.0.1"
		}
		ports = append(ports, api.Port{Protocol: forward.Protocol, Host: uint16(forward.HostPort), Guest: uint16(forward.GuestPort), HostIP: hostIP})
		coveredPorts[strconv.Itoa(forward.GuestPort)+"/"+forward.Protocol] = true
	}
	for _, required := range uniqueSorted(options.RequiredPorts) {
		if !coveredPorts[required] {
			return api.MachineSpec{}, fmt.Errorf("required OCI port %s has no explicit forwarding", required)
		}
	}
	mappedVolumes := map[string]api.Volume{}
	for _, mapping := range options.Volumes {
		if mapping.Source == "" || !filepath.IsAbs(mapping.Source) || mapping.Target == "" || !strings.HasPrefix(mapping.Target, "/") || path.Clean(mapping.Target) != mapping.Target || mapping.Target == "/" {
			return api.MachineSpec{}, fmt.Errorf("volume mapping requires absolute source and clean non-root target")
		}
		info, err := os.Lstat(mapping.Source)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return api.MachineSpec{}, fmt.Errorf("volume source %s must be an existing non-symlink file or directory", mapping.Source)
		}
		if _, duplicate := mappedVolumes[mapping.Target]; duplicate {
			return api.MachineSpec{}, fmt.Errorf("duplicate volume target %s", mapping.Target)
		}
		mappedVolumes[mapping.Target] = api.Volume{Source: mapping.Source, Target: mapping.Target, ReadOnly: mapping.ReadOnly}
	}
	requiredVolumes := uniqueSorted(options.RequiredVolumes)
	for _, required := range requiredVolumes {
		if _, present := mappedVolumes[required]; !present {
			return api.MachineSpec{}, fmt.Errorf("required OCI volume %s has no explicit host source", required)
		}
	}
	if len(mappedVolumes) != len(requiredVolumes) {
		return api.MachineSpec{}, fmt.Errorf("volume mappings contain targets not declared by the OCI image")
	}
	volumes := make([]api.Volume, 0, len(mappedVolumes))
	for _, target := range requiredVolumes {
		volumes = append(volumes, mappedVolumes[target])
	}
	dns := uniqueSorted(options.DNS)
	for _, server := range dns {
		if err := networking.ValidateDNS(server); err != nil {
			return api.MachineSpec{}, err
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Guest != ports[j].Guest {
			return ports[i].Guest < ports[j].Guest
		}
		if ports[i].Protocol != ports[j].Protocol {
			return ports[i].Protocol < ports[j].Protocol
		}
		return ports[i].Host < ports[j].Host
	})
	return api.MachineSpec{ID: options.ID, Bundle: bundle, Resources: api.Resources{VCPUs: options.VCPUs, MemoryMiB: options.MemoryMiB}, Ports: ports, Volumes: volumes, DNS: dns, Env: map[string]string{}}, nil
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

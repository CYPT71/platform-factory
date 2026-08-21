package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	microvmapp "github.com/CYPT71/platform-factory/internal/app/microvm"
	"github.com/CYPT71/platform-factory/internal/app/microvminitramfs"
	"github.com/CYPT71/platform-factory/internal/networking"
	"github.com/CYPT71/platform-factory/internal/strictjson"
)

type machineSpecCLIOptions struct {
	Name, Requirements, KernelDigest, InitramfsDigest, CommandLine string
	MemoryMiB, VCPUs                                               int
	Publishes, VolumeSources, DNS                                  []string
}

func runMachineSpec(options machineSpecCLIOptions, stdout, stderr io.Writer) int {
	if options.Requirements == "" || options.KernelDigest == "" || options.InitramfsDigest == "" {
		fmt.Fprintln(stderr, "platform-factory microvm machine-spec: --requirements, --kernel-digest and --initramfs-digest are required")
		return 2
	}
	info, err := os.Lstat(options.Requirements)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintln(stderr, "platform-factory microvm machine-spec: requirements must be an existing non-symlink regular file")
		return 2
	}
	var requirements microvminitramfs.Result
	if err := strictjson.DecodeFile(options.Requirements, &requirements); err != nil {
		fmt.Fprintf(stderr, "platform-factory microvm machine-spec: decode requirements: %v\n", err)
		return 2
	}
	forwards := make([]networking.Forward, 0, len(options.Publishes))
	for _, value := range options.Publishes {
		forward, err := networking.ParseForward(value)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory microvm machine-spec: %v\n", err)
			return 2
		}
		forwards = append(forwards, forward)
	}
	volumes := make([]microvmapp.VolumeMapping, 0, len(options.VolumeSources))
	for _, value := range options.VolumeSources {
		readOnly := strings.HasPrefix(value, "ro@")
		if readOnly {
			value = strings.TrimPrefix(value, "ro@")
		}
		target, source, found := strings.Cut(value, "=")
		if !found || target == "" || source == "" {
			fmt.Fprintln(stderr, "platform-factory microvm machine-spec: --volume-source must be TARGET=ABSOLUTE_SOURCE or ro@TARGET=ABSOLUTE_SOURCE")
			return 2
		}
		volumes = append(volumes, microvmapp.VolumeMapping{Source: source, Target: target, ReadOnly: readOnly})
	}
	commandLine := strings.Fields(options.CommandLine)
	spec, err := microvmapp.BuildMachineSpec(microvmapp.MachineSpecOptions{
		ID: options.Name, KernelDigest: options.KernelDigest, InitramfsDigest: options.InitramfsDigest,
		ManifestDigest: requirements.ManifestDigest, RootFSDigest: requirements.RootFSDigest,
		CommandLine: commandLine, MemoryMiB: uint64(options.MemoryMiB), VCPUs: uint32(options.VCPUs),
		RequiredPorts: requirements.RequiredPorts, RequiredVolumes: requirements.RequiredVolumes,
		Forwards: forwards, Volumes: volumes, DNS: options.DNS,
	})
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory microvm machine-spec: %v\n", err)
		return 1
	}
	encoded, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory microvm machine-spec: encode result: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

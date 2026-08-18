package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"

	microvmapp "github.com/CYPT71/platform-factory/internal/app/microvm"
	"github.com/CYPT71/platform-factory/internal/directboot"
	"github.com/CYPT71/platform-factory/internal/hypervisor"
	"github.com/CYPT71/platform-factory/internal/microvm"
	"github.com/CYPT71/platform-factory/internal/networking"
	"github.com/CYPT71/platform-factory/internal/vmdisk"
)

func runMicroVM(args []string, stdout, stderr io.Writer, execute microVMExecutor) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: platform-factory microvm <probe|create|delete|inspect-legacy-disk|logs|package|rbac|restart|run|run-legacy-disk|start|status|stop> [OPTIONS]")
		return 2
	}
	action := args[0]
	if action == "-h" || action == "--help" || action == "help" {
		fmt.Fprintln(stdout, "usage: platform-factory microvm <probe|create|delete|inspect-legacy-disk|logs|package|rbac|restart|run|run-legacy-disk|start|status|stop> [OPTIONS]")
		return 0
	}
	if action == "__run-native" {
		return runNativeKVMSubcommand(args[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("microvm "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	backend := flags.String("backend", "native", "backend: native or kubevirt")
	name := flags.String("name", "platform-factory", "microVM name")
	namespace := flags.String("namespace", "default", "Kubernetes namespace")
	layout := flags.String("layout", "", "verified local OCI layout (native)")
	kernel := flags.String("kernel", "", "direct-boot Linux kernel (without OCI)")
	kernelDigest := flags.String("kernel-digest", "", "sha256 digest of --kernel")
	initramfs := flags.String("initramfs", "", "direct-boot initramfs (without OCI)")
	initramfsDigest := flags.String("initramfs-digest", "", "sha256 digest of --initramfs")
	commandLine := flags.String("command-line", "", "direct-boot Linux command line")
	image := flags.String("image", "", "digest-pinned KubeVirt external-kernel-boot image")
	architecture := flags.String("arch", runtime.GOARCH, "guest architecture: amd64 or arm64 (default: host)")
	memory := flags.Int("memory-mib", 128, "guest memory in MiB")
	vcpus := flags.Int("vcpus", 1, "virtual CPU count")
	listen := flags.String("listen-address", "127.0.0.1", "host forwarding address: 127.0.0.1 or 0.0.0.0")
	var publishes repeatedFlag
	flags.Var(&publishes, "publish", "forward [IP:]HOST:GUEST[/tcp|udp]; repeatable")
	flags.Var(&publishes, "port", "alias for --publish; repeatable; a single PORT remains supported")
	flags.Var(&publishes, "p", "short alias for --publish; repeatable")
	runner := flags.String("native-runner", "scripts/microvm/run-microvm.sh", "native backend runner")
	requireNative := flags.Bool("require-native", false, "fail closed instead of falling back to the QEMU-based runner when the native KVM/HVF backend is not eligible (see nativeKVMEligible)")
	legacyDiskRunner := flags.String("legacy-disk-runner", "scripts/microvm/run-legacy-disk.sh", "legacy-disk BIOS-boot runner")
	var diskImages repeatedFlag
	flags.Var(&diskImages, "disk", "legacy VM disk image (run-legacy-disk): RAW/QCOW2/VMDK/VHD/VHDX/ISO; repeatable for a multi-disk project - boots via BIOS/OVMF and the disks' own bootloader instead of platform-factory's own kernel")
	bootDiskOverride := flags.String("boot-disk", "", "run-legacy-disk/inspect-legacy-disk: which --disk is the boot/OS disk, when it can't be (or shouldn't be) auto-detected; must match one of --disk exactly")
	reportDir := flags.String("report-dir", "reports", "inspect-legacy-disk: directory to write discovery.json/discovery.txt into")
	strategy := flags.String("strategy", "auto", "inspect-legacy-disk: auto, container, microvm-oci, microvm-direct, vm-encapsulation, or unsupported")
	bootPreparer := flags.String("boot-preparer", "scripts/microvm/prepare-kubevirt-boot.sh", "KubeVirt boot-context preparer")
	output := flags.String("output", "", "new output directory for microvm package")
	apply := flags.Bool("apply", false, "apply create output using kubectl")
	pluginFlags := registerPluginFlags(flags)
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		flags.Usage()
		return 2
	}
	if action == "run-legacy-disk" {
		if len(diskImages) == 0 {
			fmt.Fprintln(stderr, "platform-factory microvm run-legacy-disk: at least one --disk is required")
			return 2
		}
		bootIndex, disks, err := vmdisk.SelectBootDisk(diskImages, *bootDiskOverride)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory microvm run-legacy-disk: %v\n", err)
			for _, disk := range disks {
				if disk.BootScan != nil {
					fmt.Fprintf(stderr, "  %s: format=%s bootable=%v (%s)\n", disk.Path, disk.Format, disk.BootScan.Bootable, disk.BootScan.Evidence)
				} else {
					fmt.Fprintf(stderr, "  %s: format=%s boot-scan unavailable: %s\n", disk.Path, disk.Format, disk.ScanError)
				}
			}
			return 2
		}
		bootDisk := disks[bootIndex]
		fmt.Fprintf(stderr, "platform-factory microvm run-legacy-disk: boot disk=%s format=%s; booting via BIOS/OVMF - this executes the disk's own, untrusted bootloader/kernel, not platform-factory's\n", bootDisk.Path, bootDisk.Format)
		runnerArgs := []string{bootDisk.Path, string(bootDisk.Format)}
		for i, disk := range disks {
			if i == bootIndex {
				continue
			}
			fmt.Fprintf(stderr, "platform-factory microvm run-legacy-disk: attaching secondary disk=%s format=%s\n", disk.Path, disk.Format)
			runnerArgs = append(runnerArgs, disk.Path, string(disk.Format))
		}
		return executeMicroVM(*legacyDiskRunner, runnerArgs, nil, nil, stdout, stderr, execute)
	}
	if action == "inspect-legacy-disk" {
		return runInspectLegacyDisk(diskImages, *bootDiskOverride, *reportDir, vmdisk.ExecutionMode(*strategy), stdout, stderr)
	}
	if action == "probe" {
		if *backend != "native" {
			fmt.Fprintln(stderr, "platform-factory microvm probe: only the native backend can be probed")
			return 2
		}
		capabilities, err := hypervisor.ProbeNative(context.Background())
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory microvm probe: %v\n", err)
			return 1
		}
		encoded, _ := json.MarshalIndent(struct {
			APIVersion string `json:"api_version"`
			microvm.Capabilities
		}{APIVersion: cliOutputAPIVersion, Capabilities: capabilities}, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
		if !capabilities.Available {
			return 1
		}
		return 0
	}
	if action == "run" && *backend == "native" && *kernel != "" {
		if *layout != "" {
			fmt.Fprintln(stderr, "platform-factory microvm: --layout and --kernel are mutually exclusive")
			return 2
		}
		if *memory < microvm.MinMemoryMiB || *memory > microvm.MaxMemoryMiB || *vcpus < 1 || *vcpus > microvm.MaxVCPUs {
			fmt.Fprintf(stderr, "platform-factory microvm: direct boot requires %d..%d MiB and 1..%d vCPUs\n", microvm.MinMemoryMiB, microvm.MaxMemoryMiB, microvm.MaxVCPUs)
			return 2
		}
		cmdline := *commandLine
		if cmdline == "" {
			cmdline = "console=ttyS0,115200 earlycon=uart,io,0x3f8,115200 ignore_loglevel panic=0 rdinit=/sbin/init"
			if runtime.GOOS == "darwin" {
				cmdline = "console=hvc0 earlycon=hvc0 ignore_loglevel panic=0 rdinit=/sbin/init"
			}
		}
		result, err := directboot.Run(context.Background(), directboot.Config{
			KernelPath: *kernel, KernelDigest: *kernelDigest,
			InitramfsPath: *initramfs, InitramfsDigest: *initramfsDigest,
			CommandLine: cmdline, MemoryMiB: uint64(*memory), VCPUs: uint32(*vcpus),
		})
		if len(result.Serial) != 0 {
			_, _ = stdout.Write(result.Serial)
		}
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory microvm direct boot: %v\n", err)
			return 1
		}
		return 0
	}
	spec := microvm.Spec{
		Name: *name, Namespace: *namespace, Image: *image, Layout: *layout, Arch: *architecture, Listen: *listen,
		MemoryMiB: *memory, VCPUs: *vcpus, Port: 8080,
	}
	for _, value := range publishes {
		forward, err := networking.ParseForward(value)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory microvm: %v\n", err)
			return 2
		}
		spec.Forwards = append(spec.Forwards, forward)
	}
	if len(spec.Forwards) == 0 {
		spec.Forwards = []networking.Forward{{
			HostIP: spec.Listen, HostPort: spec.Port, GuestPort: spec.Port, Protocol: "tcp",
		}}
	} else {
		spec.Port = spec.Forwards[0].HostPort
	}
	if action == "package" {
		if *layout == "" || *output == "" {
			fmt.Fprintln(stderr, "platform-factory microvm package: layout and output are required")
			return 2
		}
		return executeMicroVM(*bootPreparer, []string{*layout, *output}, nil, nil, stdout, stderr, execute)
	}

	switch *backend {
	case "native":
		if action == "run" || action == "start" {
			if err := microvm.Validate(spec, "native"); err != nil {
				fmt.Fprintf(stderr, "platform-factory microvm: %v\n", err)
				return 2
			}
		} else if err := microvm.ValidateNativeTarget(spec); err != nil {
			fmt.Fprintf(stderr, "platform-factory microvm: %v\n", err)
			return 2
		}
		return runNative(action, spec, *runner, *requireNative, stdout, stderr, execute)
	case "kubevirt":
		return runKubeVirt(context.Background(), action, spec, publishes, *apply, pluginFlags, stdout, stderr)
	default:
		fmt.Fprintln(stderr, "platform-factory microvm: backend must be native or kubevirt")
		return 2
	}
}

// qemuFallbackRefused reports that --require-native forbids QEMU fallback.
func qemuFallbackRefused(reason string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "platform-factory microvm: native KVM backend not used (%s); refusing to fall back to the QEMU-based runner because --require-native was set\n", reason)
	return 1
}

func runNative(action string, spec microvm.Spec, runner string, requireNative bool, stdout, stderr io.Writer, execute microVMExecutor) int {
	unit := "platform-factory-microvm-" + spec.Name + ".service"
	switch action {
	case "run":
		eligible, reason := nativeKVMEligible(context.Background(), spec)
		if eligible {
			fmt.Fprintln(stderr, "platform-factory microvm: using native KVM backend")
			self, err := os.Executable()
			if err != nil {
				fmt.Fprintf(stderr, "platform-factory microvm: %v\n", err)
				return 1
			}
			return executeMicroVM(self, nativeRunArgs(spec), nil, nil, stdout, stderr, execute)
		}
		if requireNative {
			return qemuFallbackRefused(reason, stderr)
		}
		fmt.Fprintf(stderr, "platform-factory microvm: native KVM backend not used (%s); falling back to %s\n", reason, runner)
		return executeMicroVM(runner, []string{spec.Layout, strconv.Itoa(spec.Port)}, microvm.NativeEnvironment(spec), nil, stdout, stderr, execute)
	case "start":
		args := []string{
			"--unit=" + unit,
			"--collect",
			"--service-type=exec",
			"--property=Restart=on-failure",
			"--property=RestartSec=2s",
		}
		eligible, reason := nativeKVMEligible(context.Background(), spec)
		if eligible {
			fmt.Fprintln(stderr, "platform-factory microvm: using native KVM backend")
			self, err := os.Executable()
			if err != nil {
				fmt.Fprintf(stderr, "platform-factory microvm: %v\n", err)
				return 1
			}
			args = append(args, self)
			args = append(args, nativeRunArgs(spec)...)
			return executeMicroVM("systemd-run", args, nil, nil, stdout, stderr, execute)
		}
		if requireNative {
			return qemuFallbackRefused(reason, stderr)
		}
		fmt.Fprintf(stderr, "platform-factory microvm: native KVM backend not used (%s); falling back to %s\n", reason, runner)
		for _, value := range microvm.NativeEnvironment(spec) {
			args = append(args, "--setenv="+value)
		}
		args = append(args, runner, spec.Layout, strconv.Itoa(spec.Port))
		return executeMicroVM("systemd-run", args, nil, nil, stdout, stderr, execute)
	case "stop":
		return executeMicroVM("systemctl", []string{"stop", unit}, nil, nil, stdout, stderr, execute)
	case "restart":
		return executeMicroVM("systemctl", []string{"restart", unit}, nil, nil, stdout, stderr, execute)
	case "status":
		return executeMicroVM("systemctl", []string{
			"show", unit, "--no-pager",
			"--property=Id,ActiveState,SubState,MainPID,ExecMainStatus,RestartUSec",
		}, nil, nil, stdout, stderr, execute)
	case "logs":
		return executeMicroVM("journalctl", []string{"--unit=" + unit, "--no-pager", "--output=json"}, nil, nil, stdout, stderr, execute)
	case "delete":
		if code := executeMicroVM("systemctl", []string{"stop", unit}, nil, nil, stdout, stderr, execute); code != 0 {
			return code
		}
		return executeMicroVM("systemctl", []string{"reset-failed", unit}, nil, nil, stdout, stderr, execute)
	default:
		fmt.Fprintln(stderr, "platform-factory microvm: unsupported native action")
		return 2
	}
}

// runKubeVirt dispatches to a real, discovered, verified and sandboxed
// KubeVirt plugin by capability, rather than shelling out to a hardcoded
// binary name the way an earlier version of this function did (see
// mvp.md's own account of that gap): --backend=kubevirt is a legitimate,
// explicit user selection of which backend to drive, but which concrete
// plugin actually implements it, and whether that plugin is trusted to
// run at all, now goes through the same declared->discovered->negotiated->
// verified->available lifecycle every other plugin capability
// (detect/freeze/plan) already does, via pluginFlags (the same
// --plugin-dir/--plugin-key/--allow-*-plugin flags every other
// plugin-consuming command already exposes).
func runKubeVirt(ctx context.Context, action string, spec microvm.Spec, publishes repeatedFlag, apply bool, pluginFlags *pluginOptions, stdout, stderr io.Writer) int {
	journal, err := operationJournalFor()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory microvm: %v\n", err)
		return 1
	}
	host, err := pluginFlags.startWithJournal(ctx, journal)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory microvm: %v\n", err)
		return 1
	}
	defer host.Close()
	return dispatchKubeVirt(ctx, host, action, spec, publishes, apply, stdout, stderr)
}

// dispatchKubeVirt is runKubeVirt's testable core: given an already
// discovered-and-started pluginHost (real or, in tests, a stub - the same
// separation detect/freeze/planNotes already use), it resolves action to a
// capability, calls it and formats the result. Split out from runKubeVirt
// so tests can exercise capability routing and idempotency without a real
// plugin subprocess, the same way TestPluginHostDetectFreezeAndNotes tests
// pluginHost directly instead of through a CLI command's flag parsing.
func dispatchKubeVirt(ctx context.Context, host *pluginHost, action string, spec microvm.Spec, publishes repeatedFlag, apply bool, stdout, stderr io.Writer) int {
	capability, mutating, err := microvmapp.Capability(action)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory microvm: %v\n", err)
		return 2
	}
	client, found := host.findCapability(capability)
	if !found {
		fmt.Fprintf(stderr, "platform-factory microvm: no installed plugin provides %s; pass --plugin-dir pointing at a directory containing the kubevirt plugin (see docs/containerd-kubernetes.md)\n", capability)
		return 1
	}

	params := microvmapp.Params{
		Name: spec.Name, Namespace: spec.Namespace, Image: spec.Image, Arch: spec.Arch,
		MemoryMiB: spec.MemoryMiB, VCPUs: spec.VCPUs, ListenAddress: spec.Listen,
		Publishes: []string(publishes), Apply: apply,
	}
	var result microvmapp.Result
	if mutating {
		operationID := cliOperationID("kubevirt-microvm", action, spec.Namespace, spec.Name, spec.Image,
			strconv.Itoa(spec.MemoryMiB), strconv.Itoa(spec.VCPUs), strconv.FormatBool(apply))
		err = client.CallWithIdempotency(ctx, operationID, "v1."+capability, params, &result)
	} else {
		err = client.Call(ctx, "v1."+capability, params, &result)
	}
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory microvm: %v\n", err)
		return 1
	}
	if result.Manifest != "" {
		fmt.Fprintln(stdout, result.Manifest)
	}
	if result.Output != "" {
		fmt.Fprintln(stdout, result.Output)
	}
	return 0
}

// runInspectLegacyDisk reports read-only disk metadata without mounting or
// interpreting filesystems. Boot-disk ambiguity is reported, not fatal.
func runInspectLegacyDisk(diskImages []string, bootDiskOverride, reportDir string, strategy vmdisk.ExecutionMode, stdout, stderr io.Writer) int {
	if len(diskImages) == 0 {
		fmt.Fprintln(stderr, "platform-factory microvm inspect-legacy-disk: at least one --disk is required")
		return 2
	}
	result, err := microvmapp.InspectLegacyDisk(diskImages, bootDiskOverride, reportDir, strategy)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory microvm inspect-legacy-disk: %v\n", err)
		if errors.Is(err, microvmapp.ErrCompatibilityReport) {
			return 2
		}
		return 1
	}
	fmt.Fprint(stdout, result.Text)
	fmt.Fprint(stdout, "\n", result.CompatibilityText)
	fmt.Fprintf(stderr, "platform-factory microvm inspect-legacy-disk: wrote %s, %s, %s and %s\n", result.JSONPath, result.TextPath, result.CompatibilityJSONPath, result.CompatibilityTextPath)
	return 0
}

func executeMicroVM(command string, args, environment []string, stdin io.Reader, stdout, stderr io.Writer, execute microVMExecutor) int {
	if err := execute(command, args, environment, stdin, stdout, stderr); err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "platform-factory microvm: %v\n", err)
		return 1
	}
	return 0
}

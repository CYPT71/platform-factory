// Command platform-factory-kubevirt is the KubeVirt backend for platform-factory
// microvm: it renders VirtualMachine manifests and drives kubectl/virtctl
// through a MicroVM's KubeVirt lifecycle. cmd/platform-factory shells out to this
// binary for --backend=kubevirt rather than importing plugins/kubevirt
// directly, so the main module never depends on a runtime-engine plugin -
// see plugins/containerd/cmd/platform-factory-shim for the same boundary applied
// to containerd.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"

	"github.com/CYPT71/secure-oci-base/plugins/kubevirt"
	microvm "github.com/CYPT71/secure-oci-base/sdk/microvm"
)

type executor func(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, execCommand))
}

func execCommand(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.Command(name, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func run(args []string, stdout, stderr io.Writer, execute executor) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: platform-factory-kubevirt <create|start|stop|restart|status|logs|delete> [OPTIONS]")
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet("platform-factory-kubevirt "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "platform-factory", "microVM name")
	namespace := flags.String("namespace", "default", "Kubernetes namespace")
	image := flags.String("image", "", "digest-pinned KubeVirt external-kernel-boot image")
	architecture := flags.String("arch", runtime.GOARCH, "guest architecture: amd64 or arm64 (default: host)")
	memory := flags.Int("memory-mib", 128, "guest memory in MiB")
	vcpus := flags.Int("vcpus", 1, "virtual CPU count")
	listen := flags.String("listen-address", "127.0.0.1", "host forwarding address: 127.0.0.1 or 0.0.0.0")
	var publishes repeatedFlag
	flags.Var(&publishes, "publish", "forward [IP:]HOST:GUEST[/tcp|udp]; repeatable")
	flags.Var(&publishes, "port", "alias for --publish; repeatable")
	flags.Var(&publishes, "p", "short alias for --publish; repeatable")
	apply := flags.Bool("apply", false, "apply create output using kubectl")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		flags.Usage()
		return 2
	}

	spec := microvm.Spec{
		Name: *name, Namespace: *namespace, Image: *image, Arch: *architecture, Listen: *listen,
		MemoryMiB: *memory, VCPUs: *vcpus, Port: 8080,
	}
	for _, value := range publishes {
		forward, err := microvm.ParseForward(value)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory-kubevirt: %v\n", err)
			return 2
		}
		spec.Forwards = append(spec.Forwards, forward)
	}
	if len(spec.Forwards) == 0 {
		spec.Forwards = []microvm.Forward{{
			HostIP: spec.Listen, HostPort: spec.Port, GuestPort: spec.Port, Protocol: "tcp",
		}}
	} else {
		spec.Port = spec.Forwards[0].HostPort
	}

	var validateErr error
	if action == "create" {
		validateErr = kubevirt.Validate(spec)
	} else {
		validateErr = kubevirt.ValidateTarget(spec)
	}
	if validateErr != nil {
		fmt.Fprintf(stderr, "platform-factory-kubevirt: %v\n", validateErr)
		return 2
	}

	var command string
	var commandArgs []string
	var input io.Reader
	switch action {
	case "create":
		manifest, err := kubevirt.VirtualMachine(spec)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory-kubevirt create: %v\n", err)
			return 2
		}
		if !*apply {
			fmt.Fprintln(stdout, string(manifest))
			return 0
		}
		command, commandArgs, input = "kubectl", []string{"apply", "-f", "-"}, bytes.NewReader(manifest)
	case "start", "stop", "restart":
		command, commandArgs = "virtctl", []string{action, "--namespace", spec.Namespace, spec.Name}
	case "status":
		command, commandArgs = "kubectl", []string{"get", "virtualmachine", "--namespace", spec.Namespace, spec.Name, "-o", "json"}
	case "logs":
		command, commandArgs = "virtctl", []string{"console", "--namespace", spec.Namespace, spec.Name}
	case "delete":
		command, commandArgs = "kubectl", []string{"delete", "virtualmachine", "--namespace", spec.Namespace, spec.Name}
	default:
		fmt.Fprintln(stderr, "platform-factory-kubevirt: unsupported action")
		return 2
	}
	if err := execute(command, commandArgs, input, stdout, stderr); err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "platform-factory-kubevirt: %v\n", err)
		return 1
	}
	return 0
}

// repeatedFlag collects every occurrence of a flag that may be passed more
// than once, such as --publish.
type repeatedFlag []string

func (f *repeatedFlag) String() string { return "" }
func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

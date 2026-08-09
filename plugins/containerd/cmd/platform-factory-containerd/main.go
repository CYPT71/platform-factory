// Command platform-factory-containerd renders the containerd v2/CRI and Kubernetes
// configuration selecting platform-factory-shim (containerd-shim-platform-factory-v1)
// as the runtime for a CRI handler.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/CYPT71/secure-oci-base/plugins/containerd/internal/containerdshim"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "platform-factory-containerd:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		_, err := fmt.Fprintf(stdout, "platform-factory-containerd %s\n", version)
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: platform-factory-containerd <config|runtimeclass> [options]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	handler := flags.String("handler", containerdshim.DefaultHandler, "containerd CRI runtime handler")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	config := containerdshim.Config{Handler: *handler}
	var output string
	var err error
	switch args[0] {
	case "config":
		output, err = config.ContainerdConfig()
	case "runtimeclass":
		output, err = config.RuntimeClass()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, output)
	return err
}

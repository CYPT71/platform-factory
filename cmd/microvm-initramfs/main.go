// microvm-initramfs assembles a Linux kernel initramfs natively from a
// verified local OCI image layout - see internal/app/microvminitramfs
// for the actual conversion/pack/install logic. It never invokes an
// external tar or cpio binary.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	initramfsapp "github.com/CYPT71/platform-factory/internal/app/microvminitramfs"
)

type stringList []string

func (s *stringList) String() string { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// Run is the tested entry point; main only adapts it to os.Exit.
func Run(args []string) int { return run(args, os.Stdout, os.Stderr) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("microvm-initramfs", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layout := flags.String("layout", "", "verified local OCI image layout directory")
	platform := flags.String("platform", "linux/amd64", "manifest platform to select")
	reference := flags.String("reference", "", "manifest tag annotation to select, if the layout holds more than one")
	initBinary := flags.String("init", "", "path to the project's PID 1 binary (built from cmd/microvm-init)")
	output := flags.String("output", "", "output path for the gzip-compressed cpio initramfs")
	var entrypoint stringList
	flags.Var(&entrypoint, "entrypoint", "fixed entrypoint argument; repeatable; first must be an absolute path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *layout == "" || *initBinary == "" || *output == "" {
		fmt.Fprintln(stderr, "usage: microvm-initramfs -layout DIR -init PATH -output PATH "+
			"[-platform linux/amd64] [-reference TAG] [-entrypoint ARG]...")
		return 2
	}

	outcome, err := initramfsapp.Assemble(*layout, *platform, *reference, *initBinary, []string(entrypoint), *output)
	if err != nil {
		fmt.Fprintf(stderr, "microvm-initramfs: %v\n", err)
		return 1
	}
	encoded, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "microvm-initramfs: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

func main() {
	os.Exit(Run(os.Args[1:]))
}

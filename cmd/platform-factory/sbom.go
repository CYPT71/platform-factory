package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/CYPT71/platform-factory/internal/app/sbom"
)

// runSBOM is the CLI facade over internal/app/sbom.Service - per
// service, formats the result, and picks an exit code. Path collection
// and SBOM generation live in the service, where they're tested
// without going through the CLI at all.
func runSBOM(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sbom", flag.ContinueOnError)
	flags.SetOutput(stderr)
	outputFormat := flags.String("format", "json", "result format: json or text")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: platform-factory sbom [--format json|text] PATH [PATH...]")
		return 2
	}
	if !validOutputFormat(*outputFormat) {
		fmt.Fprintln(stderr, "platform-factory sbom: format must be json or text")
		return 2
	}

	svc := sbom.New()
	paths, err := svc.CollectPaths(flags.Args())
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory sbom: %v\n", err)
		return 1
	}
	if len(paths) == 0 {
		fmt.Fprintln(stderr, "platform-factory sbom: no regular files found in the given paths")
		return 1
	}
	document, err := svc.Generate(paths)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory sbom: %v\n", err)
		return 1
	}

	if *outputFormat == "text" {
		for _, component := range document.Components {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%d\n", component.Name, component.Kind, component.Digest, component.Size)
		}
		fmt.Fprintf(stdout, "%d components\n", len(document.Components))
		return 0
	}
	if err := svc.WriteJSON(stdout, document); err != nil {
		fmt.Fprintf(stderr, "platform-factory sbom: %v\n", err)
		return 1
	}
	return 0
}

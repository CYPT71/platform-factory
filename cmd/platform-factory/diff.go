package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/CYPT71/secure-oci-base/internal/layout"
)

// runDiff compares two OCI layouts and explains every divergence.
// Exit codes: 0 identical, 1 divergent, 2 usage or verification error.
func runDiff(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	outputFormat := flags.String("format", "json", "result format: json or text")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: platform-factory diff [--format json|text] LAYOUT_A LAYOUT_B")
		return 2
	}
	if *outputFormat != "json" && *outputFormat != "text" {
		fmt.Fprintln(stderr, "platform-factory diff: format must be json or text")
		return 2
	}
	report, err := layout.Diff(flags.Arg(0), flags.Arg(1))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory diff: %v\n", err)
		return 2
	}
	if *outputFormat == "text" {
		report.WriteText(stdout)
	} else {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	}
	if !report.Equal {
		return 1
	}
	return 0
}

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/CYPT71/secure-oci-base/internal/app/doctor"
)

// runDoctor is the CLI facade over internal/app/doctor.Service - per
// Sanetizer-todo.md item 8 ("la CLI doit devenir une façade"), it only
// parses arguments, calls the service, formats the result, and converts
// the outcome into an exit code. Every actual diagnostic - what to
// check, how to check it, what counts as OK - lives in the service,
// where it's tested without going through the CLI at all.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "machine-readable JSON output")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	report := doctor.New().Run(context.Background())

	if *jsonOutput {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory doctor: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(encoded))
		return 0
	}

	for _, c := range report.Checks {
		mark := "ok  "
		if !c.OK {
			mark = "MISS"
		}
		line := fmt.Sprintf("%s %s", mark, c.Name)
		if c.Detail != "" {
			line += " (" + c.Detail + ")"
		}
		fmt.Fprintln(stdout, line)
		if !c.OK && c.Suggestion != "" {
			fmt.Fprintf(stdout, "     -> %s\n", c.Suggestion)
		}
	}
	return 0
}

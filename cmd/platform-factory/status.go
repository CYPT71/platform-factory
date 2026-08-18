package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	statusapp "github.com/CYPT71/platform-factory/internal/app/status"
)

func runExplain(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: platform-factory explain [DIRECTORY]")
		return 2
	}
	start := "."
	if len(args) == 1 {
		start = args[0]
	}
	status := statusapp.Compute(start)
	fmt.Fprintf(stdout, "Next: %s\nWhy: %s\n", status.NextAction, statusapp.ExplainReason(status))
	return 0
}

func printStatusUsage(output io.Writer, flags *flag.FlagSet) {
	fmt.Fprintln(output, `platform-factory status — a non-mutating dashboard for the nearest project: build, evidence, publication, deployment, and exactly one safe next command

Usage:
  platform-factory status [OPTIONS] [DIRECTORY]

DIRECTORY defaults to the current directory. status never writes anything;
it is always safe to run.

Examples:
  platform-factory status
  platform-factory status --format json .
  platform-factory status ./my-project

Options:`)
	flags.SetOutput(output)
	flags.PrintDefaults()
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "result format: text or json")
	if containsHelpFlag(args) {
		printStatusUsage(stdout, flags)
		return 0
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 || (*format != "text" && *format != "json") {
		fmt.Fprintln(stderr, "usage: platform-factory status [--format text|json] [DIRECTORY]")
		return 2
	}
	start := "."
	if flags.NArg() == 1 {
		start = flags.Arg(0)
	}
	status := statusapp.Compute(start)
	if *format == "json" {
		encoded, _ := json.MarshalIndent(struct {
			APIVersion string `json:"api_version"`
			statusapp.Status
		}{APIVersion: cliOutputAPIVersion, Status: status}, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
		return 0
	}
	fmt.Fprintf(stdout, "Project     %s\n", statusValue(status.Initialized))
	fmt.Fprintf(stdout, "Build       %s\n", statusValue(status.Built))
	fmt.Fprintf(stdout, "Evidence    %s\n", statusValue(status.EvidenceComplete))
	fmt.Fprintf(stdout, "Published   %s\n", statusValue(status.Published))
	fmt.Fprintf(stdout, "Deployed    %s\n", statusValue(status.Deployed))
	if status.PublishedReference != "" {
		fmt.Fprintf(stdout, "Reference   %s\n", status.PublishedReference)
	}
	fmt.Fprintf(stdout, "Next        %s\n", status.NextAction)
	return 0
}

func statusValue(value bool) string {
	if value {
		return "ready"
	}
	return "not ready"
}

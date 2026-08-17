package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/CYPT71/platform-factory/internal/app/doctor"
)

// runDoctor is the CLI facade over internal/app/doctor.Service - per
// parses arguments, calls the service, formats the result, and converts
// the outcome into an exit code. Every actual diagnostic - what to
// check, how to check it, what counts as OK - lives in the service,
// where it's tested without going through the CLI at all.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "machine-readable JSON output")
	registry := flags.String("registry", "", "OCI registry host or URL to probe through /v2/")
	policyFile := flags.String("policy", "", "strict policy JSON file to validate")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 1 {
		fmt.Fprintln(stderr, "usage: platform-factory doctor [--json] [build|publish|deploy]")
		return 2
	}
	scope := "all"
	if flags.NArg() == 1 {
		scope = flags.Arg(0)
		if scope != "build" && scope != "publish" && scope != "deploy" {
			fmt.Fprintln(stderr, "platform-factory doctor: scope must be build, publish, or deploy")
			return 2
		}
	}

	report := doctor.New().RunScopeWithOptions(context.Background(), scope, doctor.Options{Registry: *registry, Policy: *policyFile})

	if *jsonOutput {
		encoded, err := json.MarshalIndent(struct {
			APIVersion string `json:"api_version"`
			doctor.Report
		}{APIVersion: cliOutputAPIVersion, Report: report}, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory doctor: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(encoded))
		return 0
	}

	for _, c := range report.Checks {
		mark := "ok  "
		if c.Skipped {
			mark = "skip"
		} else if !c.OK {
			mark = "MISS"
		}
		line := fmt.Sprintf("%s %s", mark, c.Name)
		if c.Detail != "" {
			line += " (" + c.Detail + ")"
		}
		fmt.Fprintln(stdout, line)
		if !c.OK && !c.Skipped && c.Suggestion != "" {
			fmt.Fprintf(stdout, "     -> %s\n", c.Suggestion)
		}
	}
	return 0
}

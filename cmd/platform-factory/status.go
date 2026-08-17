package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/project"
)

type projectStatus struct {
	APIVersion         string `json:"api_version"`
	Initialized        bool   `json:"initialized"`
	Config             string `json:"config,omitempty"`
	Built              bool   `json:"built"`
	BuildDigest        string `json:"build_digest,omitempty"`
	EvidenceComplete   bool   `json:"evidence_complete"`
	Published          bool   `json:"published"`
	PublishedReference string `json:"published_reference,omitempty"`
	Deployed           bool   `json:"deployed"`
	Deployment         string `json:"deployment,omitempty"`
	NextAction         string `json:"next_action"`
}

func runExplain(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: platform-factory explain [DIRECTORY]")
		return 2
	}
	statusArgs := []string{"--format", "json"}
	if len(args) == 1 {
		statusArgs = append(statusArgs, args[0])
	}
	var encoded bytes.Buffer
	if code := runStatus(statusArgs, &encoded, stderr); code != 0 {
		return code
	}
	var status projectStatus
	if err := json.Unmarshal(encoded.Bytes(), &status); err != nil {
		fmt.Fprintf(stderr, "platform-factory explain: decode project status: %v\n", err)
		return 1
	}
	reason := "the directory has no Platform Factory project yet"
	switch {
	case status.Deployed:
		reason = "the immutable release is deployed; inspect its bounded workload logs next"
	case status.Published:
		reason = "the signed release is published by digest but has not been deployed"
	case status.Built && status.EvidenceComplete:
		reason = "the verified release bundle is complete and ready for a registry target"
	case status.Built:
		reason = "the OCI layout exists but its release evidence is incomplete"
	case status.Initialized:
		reason = "the project is initialized but has no verified OCI build"
	}
	fmt.Fprintf(stdout, "Next: %s\nWhy: %s\n", status.NextAction, reason)
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
	status := projectStatus{APIVersion: cliOutputAPIVersion, NextAction: "pf init"}
	loaded, err := project.Discover(start, "")
	if err == nil {
		status.Initialized = true
		status.Config = loaded.File
		status.NextAction = "pf build"
		if report, verifyErr := layout.Verify(loaded.Output()); verifyErr == nil && len(report.Platforms) > 0 {
			status.Built = true
			status.BuildDigest = report.Platforms[0].Digest
			status.NextAction = "pf publish <registry/image:tag>"
		}
		releaseDir := filepath.Join(loaded.Root, ".platform-factory", "release")
		status.EvidenceComplete = regularFiles(releaseDir,
			"sbom.json", "provenance.json", "reports/build.json", "reports/policy.json",
			"reports/policy-rules.json", "reports/evidence.json", "reports/summary.txt")
		var published struct {
			APIVersion string `json:"api_version"`
			Digest     string `json:"digest"`
			Reference  string `json:"reference"`
			Repository string `json:"repository"`
			Scheme     string `json:"scheme"`
		}
		if decodeStrictJSON(filepath.Join(loaded.Root, ".platform-factory", "published.json"), &published) == nil &&
			published.APIVersion == "platform-factory.dev/publication/v1" &&
			published.Reference == published.Repository+"@"+published.Digest {
			status.Published = true
			status.PublishedReference = published.Reference
			status.NextAction = "pf deploy --dry-run"
		}
		var deployed deployedProject
		if decodeStrictJSON(filepath.Join(loaded.Root, ".platform-factory", "deployed.json"), &deployed) == nil &&
			deployed.APIVersion == "platform-factory.dev/deployment/v1" &&
			validKubernetesName(deployed.Name) && validKubernetesName(deployed.Namespace) &&
			(deployed.Workload == "job" || deployed.Workload == "service") && validDigestReference(deployed.Image) {
			status.Deployed = true
			status.Deployment = deployed.Namespace + "/" + deployed.Name
			status.NextAction = "pf logs"
		}
	}
	if *format == "json" {
		encoded, _ := json.MarshalIndent(status, "", "  ")
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

func regularFiles(root string, names ...string) bool {
	for _, name := range names {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func statusValue(value bool) string {
	if value {
		return "ready"
	}
	return "not ready"
}

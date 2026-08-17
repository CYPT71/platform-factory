package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/internal/policy"
)

func runEvidence(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("evidence", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pluginDir := flags.String("plugin-dir", "", "directory of digest-pinned plugin manifests")
	reproducible := flags.Bool("reproducible", false, "record a successful identical clean rebuild")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: platform-factory evidence [--plugin-dir DIR] [--reproducible] PIPELINE.json")
		return 2
	}
	_, document, err := decodePipelineFile(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory evidence: %v\n", err)
		return 1
	}
	var manifests []plugin.Manifest
	if *pluginDir != "" {
		discovered, err := plugin.Discover(*pluginDir)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory evidence: %v\n", err)
			return 1
		}
		for _, item := range discovered {
			if err := item.Manifest.VerifyExecutable(item.Dir); err != nil {
				fmt.Fprintf(stderr, "platform-factory evidence: %v\n", err)
				return 1
			}
			manifests = append(manifests, item.Manifest)
		}
	}
	pluginDigests := make([]string, 0, len(manifests))
	for _, manifest := range manifests {
		pluginDigests = append(pluginDigests, manifest.Digest)
	}
	evidence := policy.DerivePipelineEvidence(document.definition, pluginDigests)
	evidence.Reproducible = *reproducible
	encoded, _ := json.MarshalIndent(struct {
		APIVersion string `json:"api_version"`
		policy.Evidence
	}{APIVersion: cliOutputAPIVersion, Evidence: evidence}, "", "  ")
	fmt.Fprintln(stdout, string(encoded))
	if !evidence.SourcesPinned || !evidence.BasePinned || !evidence.ToolchainPinned ||
		!evidence.PluginsPinned || !evidence.NonRoot || !evidence.ReadOnlyRootFS {
		return 1
	}
	return 0
}

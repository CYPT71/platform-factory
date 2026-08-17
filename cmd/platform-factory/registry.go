package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/CYPT71/platform-factory/internal/registry"
)

var inspectRegistryManifest = func(ctx context.Context, target registry.Reference, digest, scheme, username, password string) ([]byte, string, error) {
	client := &registry.Client{Scheme: scheme, Username: username, Password: password}
	return client.GetManifest(ctx, target, digest)
}

func runRegistry(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "inspect" {
		fmt.Fprintln(stderr, "usage: platform-factory registry inspect [OPTIONS] IMAGE@sha256:DIGEST")
		return 2
	}
	flags := flag.NewFlagSet("registry inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "json", "result format: json or text")
	scheme := flags.String("scheme", "https", "registry scheme: https or http")
	username := flags.String("username", os.Getenv("PLATFORM_FACTORY_REGISTRY_USERNAME"), "registry username")
	password := flags.String("password", os.Getenv("PLATFORM_FACTORY_REGISTRY_PASSWORD"), "registry password")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || (*format != "json" && *format != "text") || (*scheme != "https" && *scheme != "http") {
		fmt.Fprintln(stderr, "usage: platform-factory registry inspect [--format json|text] [--scheme https|http] IMAGE@sha256:DIGEST")
		return 2
	}
	target, digest, err := registry.ParseDigestReference(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory registry inspect: %v\n", err)
		return 2
	}
	body, mediaType, err := inspectRegistryManifest(context.Background(), target, digest, *scheme, *username, *password)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory registry inspect: %v\n", err)
		return 1
	}
	sum := sha256.Sum256(body)
	report := map[string]any{"api_version": cliOutputAPIVersion, "valid": true, "reference": target.Registry + "/" + target.Repository + "@" + digest, "digest": "sha256:" + hex.EncodeToString(sum[:]), "media_type": strings.TrimSpace(strings.Split(mediaType, ";")[0]), "size": len(body)}
	if *format == "text" {
		fmt.Fprintf(stdout, "valid: %s %s %d bytes\n", report["reference"], report["media_type"], len(body))
	} else {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	}
	return 0
}

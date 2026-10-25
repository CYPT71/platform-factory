package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	pluginapp "github.com/CYPT71/platform-factory/internal/app/plugin"
	"github.com/CYPT71/platform-factory/internal/extensions"
	hostplugin "github.com/CYPT71/platform-factory/internal/plugin"
	"github.com/CYPT71/platform-factory/internal/signing"
)

func runExtensions(args []string, stdout, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, "extensions:", err); return 2 }
	if len(args) == 0 || args[0] == "--help" {
		fmt.Fprintln(stdout, "usage: pf extensions list | inspect NAME | analyze NAME --input FILE | bundle --executable TOOLKIT --output DIR --key-dir DIR")
		return 0
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fail(errors.New("list takes no arguments"))
		}
		return extensionJSON(stdout, stderr, extensions.Summaries())
	case "inspect":
		if len(args) != 2 {
			return fail(errors.New("inspect requires a plugin name"))
		}
		p, err := extensions.Lookup(args[1])
		if err != nil {
			return fail(err)
		}
		return extensionJSON(stdout, stderr, p)
	case "analyze":
		if len(args) < 2 {
			return fail(errors.New("analyze requires a plugin name"))
		}
		flags := flag.NewFlagSet("extensions analyze", flag.ContinueOnError)
		flags.SetOutput(stderr)
		input := flags.String("input", "", "JSON document file, or - for stdin")
		if err := flags.Parse(args[2:]); err != nil {
			return fail(err)
		}
		if *input == "" || flags.NArg() != 0 {
			return fail(errors.New("--input FILE is required"))
		}
		var reader io.Reader = os.Stdin
		if *input != "-" {
			file, err := os.Open(*input)
			if err != nil {
				return fail(err)
			}
			defer file.Close()
			reader = file
		}
		document, err := extensions.DecodeDocument(reader)
		if err != nil {
			return fail(err)
		}
		report, err := extensions.Analyze(args[1], document)
		if err != nil {
			return fail(err)
		}
		if code := extensionJSON(stdout, stderr, report); code != 0 {
			return code
		}
		if !report.Passed {
			return 1
		}
		return 0
	case "bundle":
		return runExtensionBundle(args[1:], stdout, stderr)
	default:
		return fail(errors.New("unknown extensions action"))
	}
}
func extensionJSON(stdout, stderr io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// Bundle all fifty from the embedded catalog, without depending on a checkout.
func runExtensionBundle(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("extensions bundle", flag.ContinueOnError)
	flags.SetOutput(stderr)
	executable := flags.String("executable", "", "prebuilt platform-factory-toolkit binary for the target OS/architecture")
	output := flags.String("output", "", "new directory for fifty signed plugin bundles")
	keyDir := flags.String("key-dir", "", "private publisher key directory outside output")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *executable == "" || *output == "" || *keyDir == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "extensions bundle: --executable, --output and --key-dir are required")
		return 2
	}
	dest, err := resolvedBundlePath(*output)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	keyPath, err := resolvedBundlePath(*keyDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rel, err := filepath.Rel(dest, keyPath)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		fmt.Fprintln(stderr, "extensions bundle: keys must be outside output")
		return 2
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, "extensions bundle: output must not exist")
		return 2
	}
	info, err := os.Lstat(*executable)
	if err != nil || !info.Mode().IsRegular() {
		fmt.Fprintln(stderr, "extensions bundle: executable must be a regular file")
		return 2
	}
	binary, err := os.ReadFile(*executable)
	if err != nil || len(binary) == 0 {
		fmt.Fprintln(stderr, "extensions bundle: cannot read nonempty binary")
		return 2
	}
	store, err := signing.NewFileKeyStore(keyPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	stage, err := os.MkdirTemp(filepath.Dir(dest), ".extensions-*")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer os.RemoveAll(stage)
	templates, err := os.MkdirTemp("", "pf-extension-templates-*")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer os.RemoveAll(templates)
	for _, p := range extensions.Catalog() {
		if err := bundleExtension(p, templates, stage, binary, store); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	// Exclusive reservation prevents overwriting existing distributions.
	if err := os.Mkdir(dest, 0700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(dest)
		}
	}()
	for _, p := range extensions.Catalog() {
		if err := os.Rename(filepath.Join(stage, p.Name), filepath.Join(dest, p.Name)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	complete = true
	return extensionJSON(stdout, stderr, map[string]any{"plugins": 50, "output": dest, "signed": true})
}
func bundleExtension(p extensions.Profile, templates, stage string, binary []byte, store signing.KeyStore) error {
	template := filepath.Join(templates, p.Name)
	if err := os.Mkdir(template, 0700); err != nil {
		return err
	}
	manifest := hostplugin.Manifest{APIVersion: hostplugin.ManifestAPIVersion, Name: p.Name, Version: "1.0.0", Family: hostplugin.PluginFamilyAnalyzer, Capabilities: []string{"analyzer.scan"}, Executable: "platform-factory-" + p.Name, Digest: "sha256:" + strings.Repeat("0", 64)}
	if len(binary) >= 2 && binary[0] == 'M' && binary[1] == 'Z' {
		manifest.Executable += ".exe"
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(template, "plugin.json"), raw, 0600); err != nil {
		return err
	}
	executable := filepath.Join(template, manifest.Executable)
	if err := os.WriteFile(executable, binary, 0700); err != nil {
		return err
	}
	defer os.Remove(executable)
	_, err = pluginapp.BuildBundle(pluginapp.BundleOptions{Source: template, Executable: executable, Output: filepath.Join(stage, p.Name), KeyName: "extension-release", Store: store})
	return err
}

// platform-factory plugin-provenance is the capture pipeline mvp.md's
// "Add verifiable build provenance of plugin" / "Associate source +
// builder + artifact + digest" checklist items asked for: given a built
// plugin executable and its source tree, it captures the source commit,
// builder identity and build inputs, and emits a signed predicate
// (internal/provenance.ProvenanceRecord) associating them with the
// executable's own digest - the same digest a plugin.json manifest pins.
// It does not modify plugin.json itself: the operator attaches the
// resulting record to a release the same way --provenance already lets
// publish attach an OCI image's own provenance as a linked artifact.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/CYPT71/platform-factory/internal/provenance"
	"github.com/CYPT71/platform-factory/internal/signing"
)

func runPluginProvenance(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin-provenance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	executable := flags.String("executable", "", "path to the built plugin executable (required)")
	name := flags.String("name", "", "plugin manifest name (required)")
	version := flags.String("version", "", "plugin manifest version")
	sourceDir := flags.String("source-dir", ".", "plugin module's source directory (must be a git checkout)")
	modulePath := flags.String("module-path", "", "plugin's Go module path (default: read from source-dir/go.mod)")
	builderID := flags.String("builder-id", "https://platform-factory.dev/builder/v1", "builder identity")
	sign := flags.Bool("sign", false, "sign the provenance record with the native Ed25519 engine")
	keyDir := flags.String("key-dir", "", "native signing key directory (default: ~/.platform-factory/keys)")
	keyName := flags.String("key-name", "plugin-provenance", "native signing key name")
	outFile := flags.String("out", "", "write the record to this file instead of stdout")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *executable == "" || *name == "" {
		fmt.Fprintln(stderr, "usage: platform-factory plugin-provenance --executable PATH --name NAME [OPTIONS]")
		return 2
	}

	digest, err := provenance.DigestPluginExecutable(*executable)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin-provenance: %v\n", err)
		return 1
	}
	commit, dirty, err := provenance.CapturePluginSourceCommit(*sourceDir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin-provenance: %v\n", err)
		return 1
	}
	module := *modulePath
	if module == "" {
		module, err = readGoModulePath(filepath.Join(*sourceDir, "go.mod"))
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory plugin-provenance: %v\n", err)
			return 1
		}
	}

	record, err := provenance.GeneratePluginProvenance(provenance.PluginBuildInputs{
		PluginName: *name, PluginVersion: *version, ArtifactDigest: digest,
		SourceCommit: commit, SourceDirty: dirty, ModulePath: module,
		GoVersion: runtime.Version(), BuilderID: *builderID,
	})
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin-provenance: %v\n", err)
		return 1
	}

	if *sign {
		dir := *keyDir
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				fmt.Fprintf(stderr, "platform-factory plugin-provenance: %v\n", err)
				return 1
			}
			dir = filepath.Join(home, ".platform-factory", "keys")
		}
		store, err := signing.NewFileKeyStore(dir)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory plugin-provenance: %v\n", err)
			return 1
		}
		record, err = provenance.SignPluginProvenance(record, store, *keyName)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory plugin-provenance: %v\n", err)
			return 1
		}
	}

	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin-provenance: %v\n", err)
		return 1
	}
	if *outFile != "" {
		if err := os.WriteFile(*outFile, append(encoded, '\n'), 0o644); err != nil {
			fmt.Fprintf(stderr, "platform-factory plugin-provenance: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

// readGoModulePath reads the module directive from a go.mod file - the
// same information `go list -m` reports, read directly so this command has
// no dependency on the go toolchain being importable/runnable in whatever
// environment captures provenance.
func readGoModulePath(goModPath string) (string, error) {
	file, err := os.Open(goModPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w (pass --module-path explicitly if the plugin source isn't a Go module root)", goModPath, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if module, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(module), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %s: %w", goModPath, err)
	}
	return "", fmt.Errorf("%s has no module directive", goModPath)
}

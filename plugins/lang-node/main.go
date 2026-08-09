// platform-factory-lang-node is the Node.js language plugin - see
// plugins/lang-python/main.go for the full pattern this mirrors and
// docs/language-plugin-layers.md for the architecture. Only the
// freeze/deps-location specifics differ per language; every plugin
// shares its tar-packaging logic via sdk/langplugin instead of
// duplicating it.
//
//	platform-factory-lang-node freeze --root DIR
//	platform-factory-lang-node build-layer --root DIR --output TAR --dest PREFIX
//
// freeze mirrors the host's own built-in Node freeze step exactly: `npm
// ci --ignore-scripts` when a lockfile already exists, otherwise `npm
// install --package-lock-only --ignore-scripts` followed by `npm ci
// --ignore-scripts`. npm's own default install location, ./node_modules,
// needs no redirection - unlike Java/dotnet/Rust, Node has no global
// package cache to opt out of.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/CYPT71/secure-oci-base/sdk/langplugin"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "freeze":
		err = runFreeze(os.Args[2:])
	case "build-layer":
		err = runBuildLayer(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-factory-lang-node: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-node <freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

// depsRelPath is npm's own default install location - identical to the
// host's built-in Node freeze step, no redirection needed or applied.
const depsRelPath = "node_modules"

func runFreeze(args []string) error {
	root, err := parseRootFlag("freeze", args)
	if err != nil {
		return err
	}
	hasLockfile := fileExists(filepath.Join(root, "package-lock.json")) || fileExists(filepath.Join(root, "npm-shrinkwrap.json"))
	if hasLockfile {
		return runIn(root, "npm", "ci", "--ignore-scripts")
	}
	if err := runIn(root, "npm", "install", "--package-lock-only", "--ignore-scripts"); err != nil {
		return fmt.Errorf("npm install --package-lock-only: %w", err)
	}
	if err := runIn(root, "npm", "ci", "--ignore-scripts"); err != nil {
		return fmt.Errorf("npm ci: %w", err)
	}
	return nil
}

func runBuildLayer(args []string) error {
	root, output, dest, err := parseBuildLayerFlags(args)
	if err != nil {
		return err
	}
	source := filepath.Join(root, depsRelPath)
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("%s does not exist - run `platform-factory-lang-node freeze` first: %w", depsRelPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", depsRelPath)
	}
	return langplugin.WriteDeterministicTar(source, dest, output)
}
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func runIn(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func parseRootFlag(subcommand string, args []string) (root string, err error) {
	flags := flag.NewFlagSet(subcommand, flag.ContinueOnError)
	rootFlag := flags.String("root", "", "project root directory")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if *rootFlag == "" {
		return "", errors.New("--root is required")
	}
	return *rootFlag, nil
}

func parseBuildLayerFlags(args []string) (root, output, dest string, err error) {
	flags := flag.NewFlagSet("build-layer", flag.ContinueOnError)
	rootFlag := flags.String("root", "", "project root directory")
	outputFlag := flags.String("output", "", "path to write the uncompressed tar layer to")
	destFlag := flags.String("dest", "", "container path prefix every entry in the layer is rooted at")
	if err := flags.Parse(args); err != nil {
		return "", "", "", err
	}
	if *rootFlag == "" || *outputFlag == "" || *destFlag == "" {
		return "", "", "", errors.New("--root, --output, and --dest are all required")
	}
	return *rootFlag, *outputFlag, *destFlag, nil
}

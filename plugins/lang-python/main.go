// platform-factory-lang-python is the Python reference implementation of
// the language-plugin pattern described in docs/language-plugin-layers.md:
// a separate Go module and binary (matching plugins/containerd and
// plugins/kubevirt's own out-of-module pattern), not the sdk/plugin
// subprocess-RPC protocol examples/sdk/plugin-python demonstrates. It
// never runs unless a project's platform-factory.yaml explicitly sets
// language_plugin: true - see cmd/platform-factory/language_plugin.go
// for the host-side dispatch this binary is invoked from.
//
// Two subcommands:
//
//	platform-factory-lang-python freeze --root DIR
//	platform-factory-lang-python build-layer --root DIR --output TAR --dest PREFIX
//
// freeze mirrors the host's own built-in Python freeze step exactly
// (pip install --target, then pip freeze > requirements.lock) so a
// project can opt into this plugin without its freeze behavior changing
// at all. build-layer is the new capability: it packages whatever
// freeze already installed as a standalone, uncompressed tar - the host
// (internal/oci.Build via Options.ExtraLayers) independently validates
// and re-hashes every byte of it before trusting any of it; nothing
// this binary prints or its own exit code is taken on faith.
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
		fmt.Fprintf(os.Stderr, "platform-factory-lang-python: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-python <freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

// depsRelPath is where freeze installs packages and build-layer reads
// them from, relative to --root - identical to the host's own built-in
// Python freeze step (cmd/platform-factory/project.go's freezeSteps),
// so switching a project to this plugin changes nothing about where
// dependencies end up during freeze, only how they reach the image.
const depsRelPath = ".platform-factory/deps/python"

func runFreeze(args []string) error {
	root, err := parseRootFlag("freeze", args)
	if err != nil {
		return err
	}
	requirements := "requirements.lock"
	if !fileExists(filepath.Join(root, requirements)) {
		requirements = "requirements.txt"
	}
	if !fileExists(filepath.Join(root, requirements)) {
		return fmt.Errorf("no requirements.lock or requirements.txt found in %s", root)
	}
	target := filepath.Join(root, depsRelPath)
	if err := runIn(root, "python", "-m", "pip", "install", "--requirement", requirements, "--target", depsRelPath); err != nil {
		return fmt.Errorf("pip install: %w", err)
	}
	lockFile, err := os.Create(filepath.Join(root, "requirements.lock"))
	if err != nil {
		return fmt.Errorf("create requirements.lock: %w", err)
	}
	defer lockFile.Close()
	cmd := exec.Command("python", "-m", "pip", "freeze", "--path", target)
	cmd.Dir = root
	cmd.Stdout = lockFile
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pip freeze: %w", err)
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
		return fmt.Errorf("%s does not exist - run `platform-factory-lang-python freeze` first: %w", depsRelPath, err)
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

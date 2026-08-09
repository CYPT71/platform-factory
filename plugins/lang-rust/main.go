// platform-factory-lang-rust is the Rust language plugin - see
// plugins/lang-python/main.go for the full pattern this mirrors and
// docs/language-plugin-layers.md for the architecture. Only the
// freeze/deps-location specifics differ per language; every plugin
// shares its tar-packaging logic via sdk/langplugin instead of
// duplicating it.
//
//	platform-factory-lang-rust freeze --root DIR
//	platform-factory-lang-rust build-layer --root DIR --output TAR --dest PREFIX
//
// freeze is a deliberate deviation from the host's built-in Rust freeze
// step (`cargo generate-lockfile` + `cargo fetch --locked`, see
// cmd/platform-factory/project.go's freezeSteps): Cargo defaults to a
// shared, unbounded, per-user global registry cache (~/.cargo/registry)
// that can't be packaged into a layer as-is. This plugin sets
// CARGO_HOME=<root>/.platform-factory/deps/rust on the command instead,
// redirecting the fetch into a project-local directory, matching the
// same redirection pattern used for Java/dotnet.
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
		fmt.Fprintf(os.Stderr, "platform-factory-lang-rust: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-rust <freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

// depsRelPath is the project-local directory this plugin redirects
// Cargo's registry cache into - see the package doc comment for why
// redirection is necessary here but not for the other built-in
// languages.
const depsRelPath = ".platform-factory/deps/rust"

func runFreeze(args []string) error {
	root, err := parseRootFlag("freeze", args)
	if err != nil {
		return err
	}
	cargoHome := "CARGO_HOME=" + filepath.Join(root, depsRelPath)
	if err := runInWithEnv(root, []string{cargoHome}, "cargo", "generate-lockfile"); err != nil {
		return fmt.Errorf("cargo generate-lockfile: %w", err)
	}
	if err := runInWithEnv(root, []string{cargoHome}, "cargo", "fetch", "--locked"); err != nil {
		return fmt.Errorf("cargo fetch --locked: %w", err)
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
		return fmt.Errorf("%s does not exist - run `platform-factory-lang-rust freeze` first: %w", depsRelPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", depsRelPath)
	}
	return langplugin.WriteDeterministicTar(source, dest, output)
}
func runInWithEnv(dir string, extraEnv []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
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

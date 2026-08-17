package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

func main() {
	err := langplugin.Dispatch(os.Args[1:], map[string]langplugin.Handler{
		"inspect": runInspect, "freeze": runFreeze, "build-layer": runBuildLayer,
	})
	if err == langplugin.ErrUsage {
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-factory-lang-rust: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-rust <inspect|freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  inspect --root DIR")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

func runInspect(args []string) error {
	root, err := langplugin.ParseRootFlag("inspect", args)
	if err != nil {
		return err
	}
	result, err := langplugin.Inspect(root, langplugin.Definition{Language: "rust", Profile: "static", Markers: []string{"Cargo.toml", "Cargo.lock"}, SourceExtensions: []string{".rs"}, Manifests: []string{"Cargo.toml"}})
	if err != nil {
		return err
	}
	return langplugin.WriteInspection(result)
}

// depsRelPath is the project-local directory this plugin redirects
// Cargo's registry cache into - see the package doc comment for why
// redirection is necessary here but not for the other built-in
// languages.
const depsRelPath = ".platform-factory/deps/rust"

func runFreeze(args []string) error {
	root, err := langplugin.ParseRootFlag("freeze", args)
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
	return langplugin.BuildLayer(args, depsRelPath, "platform-factory-lang-rust")
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

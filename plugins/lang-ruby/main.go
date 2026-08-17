package main

import (
	"fmt"
	"os"

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
		fmt.Fprintf(os.Stderr, "platform-factory-lang-ruby: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-ruby <inspect|freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  inspect --root DIR")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

func runInspect(args []string) error {
	root, err := langplugin.ParseRootFlag("inspect", args)
	if err != nil {
		return err
	}
	result, err := langplugin.Inspect(root, langplugin.Definition{Language: "ruby", Profile: "ruby", Markers: []string{"Gemfile", "Gemfile.lock"}, SourceExtensions: []string{".rb"}, Entrypoints: []string{"app.rb", "main.rb"}, Manifests: []string{"Gemfile"}})
	if err != nil {
		return err
	}
	return langplugin.WriteInspection(result)
}

// depsRelPath is bundle cache's own default vendor location - identical
// to the host's built-in Ruby freeze step, no redirection needed or
// applied.
const depsRelPath = "vendor/cache"

func runFreeze(args []string) error {
	root, err := langplugin.ParseRootFlag("freeze", args)
	if err != nil {
		return err
	}
	if err := langplugin.RunIn(root, "bundle", "lock"); err != nil {
		return fmt.Errorf("bundle lock: %w", err)
	}
	if err := langplugin.RunIn(root, "bundle", "cache", "--all"); err != nil {
		return fmt.Errorf("bundle cache --all: %w", err)
	}
	return nil
}

func runBuildLayer(args []string) error {
	return langplugin.BuildLayer(args, depsRelPath, "platform-factory-lang-ruby")
}

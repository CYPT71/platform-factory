package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

func main() {
	err := langplugin.Dispatch(os.Args[1:], map[string]langplugin.Handler{
		"inspect": runInspect, "scaffold": runScaffold,
		"freeze": runFreeze, "build-layer": runBuildLayer,
	})
	if err == langplugin.ErrUsage {
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-factory-lang-php: %v\n", err)
		os.Exit(1)
	}
}

func runScaffold(args []string) error {
	flags := flag.NewFlagSet("scaffold", flag.ContinueOnError)
	name := flags.String("name", "", "plugin name")
	output := flags.String("output", "", "output directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *name == "" || *output == "" {
		return errors.New("--name and --output are required")
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(*output)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("output directory must be empty")
	}
	source := fmt.Sprintf("#!/usr/bin/env php\n<?php echo json_encode(['match'=>false,'language'=>'%s','profile'=>'unknown','evidence'=>[],'dependencies'=>['mode'=>'unknown','reason'=>'customize me']]);\n", *name)
	path := filepath.Join(*output, "plugin.php")
	if err := os.WriteFile(path, []byte(source), 0o755); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-php <inspect|freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  inspect --root DIR")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

func runInspect(args []string) error {
	root, err := langplugin.ParseRootFlag("inspect", args)
	if err != nil {
		return err
	}
	result, err := langplugin.Inspect(root, langplugin.Definition{Language: "php", Profile: "php", Markers: []string{"composer.json", "composer.lock"}, SourceExtensions: []string{".php"}, Entrypoints: []string{"public/index.php", "index.php"}, Manifests: []string{"composer.json"}})
	if err != nil {
		return err
	}
	return langplugin.WriteInspection(result)
}

// depsRelPath is Composer's own default install location - identical to
// the host's built-in PHP freeze step, no redirection needed or applied.
const depsRelPath = "vendor"

func runFreeze(args []string) error {
	root, err := langplugin.ParseRootFlag("freeze", args)
	if err != nil {
		return err
	}
	if err := langplugin.RunIn(root, "composer", "install", "--no-dev", "--prefer-dist", "--no-interaction"); err != nil {
		return fmt.Errorf("composer install: %w", err)
	}
	return nil
}

func runBuildLayer(args []string) error {
	return langplugin.BuildLayer(args, depsRelPath, "platform-factory-lang-php")
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		fmt.Fprintf(os.Stderr, "platform-factory-lang-node: %v\n", err)
		os.Exit(1)
	}
}

func runScaffold(args []string) error {
	flags := flag.NewFlagSet("scaffold", flag.ContinueOnError)
	name := flags.String("name", "", "plugin name")
	output := flags.String("output", "", "output directory")
	dialect := flags.String("dialect", "js", "js or ts")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *name == "" || *output == "" {
		return errors.New("--name and --output are required")
	}
	if *dialect != "js" && *dialect != "ts" {
		return errors.New("--dialect must be js or ts")
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
	ext := "js"
	declaration := "const result ="
	if *dialect == "ts" {
		ext = "ts"
		declaration = "const result: object ="
	}
	source := fmt.Sprintf("#!/usr/bin/env node\n%s {match:false,language:%q,profile:'unknown',evidence:[],dependencies:{mode:'unknown',reason:'customize me'}};\nconsole.log(JSON.stringify(result));\n", declaration, *name)
	path := filepath.Join(*output, "plugin."+ext)
	if err := os.WriteFile(path, []byte(source), 0o755); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-node <inspect|freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  inspect --root DIR")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

func runInspect(args []string) error {
	root, err := langplugin.ParseRootFlag("inspect", args)
	if err != nil {
		return err
	}
	result, err := langplugin.Inspect(root, langplugin.Definition{Language: "node", Profile: "node", Markers: []string{"package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock"}, SourceExtensions: []string{".js", ".mjs", ".cjs", ".ts"}, Entrypoints: []string{"index.js", "app.js", "server.js"}, Manifests: []string{"package.json"}, Imports: nodeImports})
	if err != nil {
		return err
	}
	return langplugin.WriteInspection(result)
}
func nodeImports(source string) ([]string, bool) {
	var imports []string
	dynamic := strings.Contains(source, "import(")
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if (strings.HasPrefix(line, "import ") && strings.Contains(line, " from ")) || strings.Contains(line, "require(") {
			if !strings.Contains(line, `"./`) && !strings.Contains(line, `"../`) && !strings.Contains(line, `'./`) && !strings.Contains(line, `'../`) && !strings.Contains(line, "node:") {
				imports = append(imports, "external-module")
			}
		}
	}
	return imports, dynamic
}

// depsRelPath is npm's own default install location - identical to the
// host's built-in Node freeze step, no redirection needed or applied.
const depsRelPath = "node_modules"

func runFreeze(args []string) error {
	root, err := langplugin.ParseRootFlag("freeze", args)
	if err != nil {
		return err
	}
	hasLockfile := langplugin.FileExists(filepath.Join(root, "package-lock.json")) || langplugin.FileExists(filepath.Join(root, "npm-shrinkwrap.json"))
	if hasLockfile {
		return langplugin.RunIn(root, "npm", "ci", "--ignore-scripts")
	}
	if err := langplugin.RunIn(root, "npm", "install", "--package-lock-only", "--ignore-scripts"); err != nil {
		return fmt.Errorf("npm install --package-lock-only: %w", err)
	}
	if err := langplugin.RunIn(root, "npm", "ci", "--ignore-scripts"); err != nil {
		return fmt.Errorf("npm ci: %w", err)
	}
	return nil
}

func runBuildLayer(args []string) error {
	return langplugin.BuildLayer(args, depsRelPath, "platform-factory-lang-node")
}

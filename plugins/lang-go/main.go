package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

const depsRelPath = "vendor"

func main() {
	err := langplugin.Dispatch(os.Args[1:], map[string]langplugin.Handler{
		"inspect": runInspect, "freeze": runFreeze, "build-layer": runBuildLayer,
	})
	if err == langplugin.ErrUsage {
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-factory-lang-go: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-go <inspect|freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  inspect --root DIR")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

func runInspect(args []string) error {
	root, err := langplugin.ParseRootFlag("inspect", args)
	if err != nil {
		return err
	}
	result, err := langplugin.Inspect(root, langplugin.Definition{Language: "go", Profile: "static", Markers: []string{"go.mod"}, SourceExtensions: []string{".go"}, Manifests: []string{"go.mod"}, Entrypoints: []string{"main.go"}, Infer: func(root string, sources []string) (string, string) {
		for _, source := range sources {
			if source == "main.go" {
				return "go\x00build\x00-o\x00app\x00main.go", "app"
			}
		}
		return "", ""
	}, Imports: goImports})
	if err != nil {
		return err
	}
	return langplugin.WriteInspection(result)
}

func runFreeze(args []string) error {
	root, err := langplugin.ParseRootFlag("freeze", args)
	if err != nil {
		return err
	}
	if !langplugin.FileExists(filepath.Join(root, "go.mod")) {
		return fmt.Errorf("no go.mod found in %s", root)
	}
	if err := langplugin.RunIn(root, "go", "mod", "tidy"); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}
	if err := langplugin.RunIn(root, "go", "mod", "vendor"); err != nil {
		return fmt.Errorf("go mod vendor: %w", err)
	}
	return os.MkdirAll(filepath.Join(root, depsRelPath), 0o755)
}

func runBuildLayer(args []string) error {
	return langplugin.BuildLayer(args, depsRelPath, "platform-factory-lang-go")
}

func goImports(source string) ([]string, bool) {
	var imports []string
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"`) {
			module := strings.Trim(line, `"`)
			if strings.Contains(module, ".") {
				imports = append(imports, module)
			}
		}
	}
	return imports, false
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	if os.Args[1] != "inspect" {
		usage()
		os.Exit(2)
	}
	if err := runInspect(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "platform-factory-lang-go: %v\n", err)
		os.Exit(1)
	}
}
func usage() { fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-go inspect --root DIR") }
func runInspect(args []string) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	root := flags.String("root", "", "project root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("--root is required")
	}
	result, err := langplugin.Inspect(*root, langplugin.Definition{Language: "go", Profile: "static", Markers: []string{"go.mod"}, SourceExtensions: []string{".go"}, Manifests: []string{"go.mod"}, Entrypoints: []string{"main.go"}, Infer: func(root string, sources []string) (string, string) {
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

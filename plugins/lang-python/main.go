package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

func main() {
	err := langplugin.Dispatch(os.Args[1:], map[string]langplugin.Handler{
		"inspect": runInspect, "scaffold": runScaffold,
		"freeze": runFreeze, "build-layer": runBuildLayer,
		"runtime": runRuntime,
	})
	if err == langplugin.ErrUsage {
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-factory-lang-python: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-python <inspect|scaffold|freeze|build-layer|runtime> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  inspect --root DIR")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
	fmt.Fprintln(os.Stderr, "  runtime --root DIR --image-root DIR [--interpreter PATH]")
}

func runScaffold(args []string) error {
	flags := flag.NewFlagSet("scaffold", flag.ContinueOnError)
	name := flags.String("name", "", "new plugin name")
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
	source := fmt.Sprintf(`#!/usr/bin/env python3
import json
import sys

def inspect(root):
    return {"match": False, "language": %q, "profile": "unknown",
            "evidence": [], "dependencies": {"mode": "unknown", "reason": "customize me"}}

if __name__ == "__main__":
    if len(sys.argv) < 2 or sys.argv[1] != "inspect":
        raise SystemExit("usage: plugin.py inspect --root DIR")
    print(json.dumps(inspect(sys.argv[-1])))
`, *name)
	path := filepath.Join(*output, "plugin.py")
	if err := os.WriteFile(path, []byte(source), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "README.md"), []byte("# "+*name+"\n\nEdit `inspect`, then run `pf plugin load --from ./plugin.py "+*name+"`.\n"), 0o644); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func runInspect(args []string) error {
	root, err := langplugin.ParseRootFlag("inspect", args)
	if err != nil {
		return err
	}
	result, err := langplugin.Inspect(root, langplugin.Definition{Language: "python", Profile: "python", Markers: []string{"pyproject.toml", "requirements.txt", "Pipfile"}, SourceExtensions: []string{".py"}, Entrypoints: []string{"app.py", "main.py", "__main__.py"}, Manifests: []string{"pyproject.toml", "requirements.txt", "Pipfile"}, Imports: pythonImports})
	if err != nil {
		return err
	}
	return langplugin.WriteInspection(result)
}

func pythonImports(source string) ([]string, bool) {
	standard := map[string]bool{"collections": true, "contextlib": true, "datetime": true, "functools": true, "http": true, "io": true, "json": true, "logging": true, "math": true, "os": true, "pathlib": true, "re": true, "subprocess": true, "sys": true, "time": true, "typing": true, "unittest": true}
	var imports []string
	dynamic := strings.Contains(source, "importlib.import_module") || strings.Contains(source, "__import__(")
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "from ") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				module := strings.Split(fields[1], ".")[0]
				if !standard[module] {
					imports = append(imports, module)
				}
			}
		}
	}
	return imports, dynamic
}

// depsRelPath is where freeze installs packages and build-layer reads
// them from, relative to --root - identical to the host's own built-in
// Python freeze step (cmd/platform-factory/project.go's freezeSteps),
// so switching a project to this plugin changes nothing about where
// dependencies end up during freeze, only how they reach the image.
const depsRelPath = ".platform-factory/deps/python"

func runFreeze(args []string) error {
	root, err := langplugin.ParseRootFlag("freeze", args)
	if err != nil {
		return err
	}
	requirements := "requirements.lock"
	if !langplugin.FileExists(filepath.Join(root, requirements)) {
		requirements = "requirements.txt"
	}
	if !langplugin.FileExists(filepath.Join(root, requirements)) {
		return fmt.Errorf("no requirements.lock or requirements.txt found in %s", root)
	}
	target := filepath.Join(root, depsRelPath)
	if err := langplugin.RunIn(root, "python", "-m", "pip", "install", "--requirement", requirements, "--target", depsRelPath); err != nil {
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
	return langplugin.BuildLayer(args, depsRelPath, "platform-factory-lang-python")
}

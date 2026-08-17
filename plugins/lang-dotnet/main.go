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
		fmt.Fprintf(os.Stderr, "platform-factory-lang-dotnet: %v\n", err)
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
	project := "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework><ImplicitUsings>enable</ImplicitUsings></PropertyGroup></Project>\n"
	source := fmt.Sprintf("using System.Text.Json; Console.WriteLine(JsonSerializer.Serialize(new { match=false, language=%q, profile=\"unknown\", evidence=Array.Empty<string>(), dependencies=new { mode=\"unknown\", reason=\"customize me\" } }));\n", *name)
	if err := os.WriteFile(filepath.Join(*output, "Plugin.csproj"), []byte(project), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*output, "Program.cs"), []byte(source), 0o644); err != nil {
		return err
	}
	fmt.Println(filepath.Join(*output, "Plugin.csproj"))
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: platform-factory-lang-dotnet <inspect|freeze|build-layer> [OPTIONS]")
	fmt.Fprintln(os.Stderr, "  inspect --root DIR")
	fmt.Fprintln(os.Stderr, "  freeze --root DIR")
	fmt.Fprintln(os.Stderr, "  build-layer --root DIR --output TAR --dest PREFIX")
}

func runInspect(args []string) error {
	root, err := langplugin.ParseRootFlag("inspect", args)
	if err != nil {
		return err
	}
	result, err := langplugin.Inspect(root, langplugin.Definition{Language: "dotnet", Profile: "dotnet", Markers: []string{"*.csproj", "*.fsproj", "global.json"}, SourceExtensions: []string{".cs", ".fs"}, Manifests: []string{"*.csproj", "*.fsproj", "global.json"}, Infer: func(root string, sources []string) (string, string) {
		matches, _ := filepath.Glob(filepath.Join(root, "*.*proj"))
		if len(matches) == 0 {
			return "", ""
		}
		project := filepath.Base(matches[0])
		assembly := project[:len(project)-len(filepath.Ext(project))] + ".dll"
		return strings.Join([]string{"dotnet", "publish", project, "-c", "Release", "-o", ".platform-factory/build", "--artifacts-path", ".platform-factory/artifacts"}, "\x00"), filepath.ToSlash(filepath.Join(".platform-factory", "build", assembly))
	}})
	if err != nil {
		return err
	}
	return langplugin.WriteInspection(result)
}

// depsRelPath is the project-local directory this plugin redirects
// NuGet's restore into - see the package doc comment for why
// redirection is necessary here but not for the other built-in
// languages.
const depsRelPath = ".platform-factory/deps/dotnet"

func runFreeze(args []string) error {
	root, err := langplugin.ParseRootFlag("freeze", args)
	if err != nil {
		return err
	}
	packages := filepath.Join(root, depsRelPath)
	if err := langplugin.RunIn(root, "dotnet", "restore", "--use-lock-file", "--packages", packages); err != nil {
		return fmt.Errorf("dotnet restore: %w", err)
	}
	return nil
}

func runBuildLayer(args []string) error {
	return langplugin.BuildLayer(args, depsRelPath, "platform-factory-lang-dotnet")
}

package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/CYPT71/platform-factory/internal/project"
)

// FreezeStep is one command `pf project freeze` runs, and where to send
// its stdout: to the process's own stderr (the default, Output == ""),
// or captured into a file relative to the project root (used only by
// the Python case below, which must generate requirements.lock from
// pip's own stdout rather than a file pip writes itself).
type FreezeStep struct {
	Args   []string
	Output string
}

// ErrNoBuiltinFreezeAdapter marks FreezeSteps' default case so a caller
// can detect it with errors.Is rather than matching on the error's
// user-facing text - that text is free to be rewritten for clarity
// without silently breaking a plugin-freeze fallback built on top of
// this sentinel.
var ErrNoBuiltinFreezeAdapter = errors.New("no built-in freeze adapter")

// FreezeSteps resolves loaded's built-in dependency-freeze commands:
// which package-manager commands pin language's dependencies at
// loaded.Root, and, for Python, where to capture the resulting lock
// file. Which lockfile/command set applies for node, java, and python
// depends on what's already present at loaded.Root (see the "exists"
// checks below) - a lockfile that's already present is trusted over
// generating a new one from scratch.
func FreezeSteps(loaded project.Loaded) ([]FreezeStep, error) {
	if loaded.Config.DependencyManagement != nil {
		switch loaded.Config.DependencyManagement.Mode {
		case "none", "external":
			return nil, nil
		case "unresolved", "unknown":
			return nil, fmt.Errorf("dependency state is %s; resolve it before freezing", loaded.Config.DependencyManagement.Mode)
		}
	}
	if len(loaded.Config.FreezeCommand) > 0 {
		return []FreezeStep{{Args: loaded.Config.FreezeCommand}}, nil
	}
	exists := func(name string) bool {
		info, err := os.Stat(filepath.Join(loaded.Root, name))
		return err == nil && info.Mode().IsRegular()
	}
	switch strings.ToLower(loaded.Config.Language) {
	case "go", "golang":
		return []FreezeStep{{Args: []string{"go", "mod", "tidy"}}, {Args: []string{"go", "mod", "vendor"}}}, nil
	case "node", "nodejs", "javascript", "typescript":
		if exists("package-lock.json") || exists("npm-shrinkwrap.json") {
			return []FreezeStep{{Args: []string{"npm", "ci", "--ignore-scripts"}}}, nil
		}
		return []FreezeStep{
			{Args: []string{"npm", "install", "--package-lock-only", "--ignore-scripts"}},
			{Args: []string{"npm", "ci", "--ignore-scripts"}},
		}, nil
	case "python":
		requirements := "requirements.lock"
		if !exists(requirements) {
			requirements = "requirements.txt"
		}
		if !exists(requirements) {
			return nil, errors.New("no requirements.lock or requirements.txt found here - " +
				"add one of those files, or add a freeze_command to platform-factory.yaml, e.g.:\n" +
				"  freeze_command: [\"pip\", \"freeze\", \">\", \"requirements.txt\"]")
		}
		target := ".platform-factory/deps/python"
		return []FreezeStep{
			{Args: []string{"python", "-m", "pip", "install", "--requirement", requirements, "--target", target}},
			{Args: []string{"python", "-m", "pip", "freeze", "--path", target}, Output: "requirements.lock"},
		}, nil
	case "java":
		if exists("mvnw") {
			return []FreezeStep{{Args: []string{wrapperCommand("./mvnw"), "-B", "dependency:go-offline"}}}, nil
		}
		if exists("gradlew") {
			return []FreezeStep{{Args: []string{wrapperCommand("./gradlew"), "dependencies", "--write-locks"}}}, nil
		}
		if exists("pom.xml") {
			return []FreezeStep{{Args: []string{"mvn", "-B", "dependency:go-offline"}}}, nil
		}
		return nil, errors.New("no Maven (pom.xml/mvnw) or Gradle (gradlew) files found here - " +
			"add one of those, or add a freeze_command to platform-factory.yaml")
	case "dotnet", "csharp", "fsharp":
		return []FreezeStep{{Args: []string{"dotnet", "restore", "--use-lock-file"}}}, nil
	case "rust":
		return []FreezeStep{{Args: []string{"cargo", "generate-lockfile"}}, {Args: []string{"cargo", "fetch", "--locked"}}}, nil
	case "ruby":
		return []FreezeStep{{Args: []string{"bundle", "lock"}}, {Args: []string{"bundle", "cache", "--all"}}}, nil
	case "php":
		return []FreezeStep{{Args: []string{"composer", "install", "--no-dev", "--prefer-dist", "--no-interaction"}}}, nil
	case "compiled":
		return nil, nil
	case "custom":
		return nil, errors.New(`language: custom means you write the freeze step yourself - add a freeze_command to platform-factory.yaml, e.g.:` +
			"\n  freeze_command: [\"make\", \"deps\"]")
	default:
		return nil, fmt.Errorf("%w: %q isn't a language platform-factory knows how to freeze automatically (try: go, node, python, java, dotnet, rust, ruby, php, or custom) - "+
			"add a freeze_command to platform-factory.yaml instead", ErrNoBuiltinFreezeAdapter, loaded.Config.Language)
	}
}

func wrapperCommand(value string) string {
	if runtime.GOOS == "windows" {
		return value + ".bat"
	}
	return value
}

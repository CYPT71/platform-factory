// Package languageplugin is the application-layer service behind
// cmd/platform-factory-plugin-languages, the official out-of-process
// language adapter plugin: which package-manager commands freeze a
// project's dependencies for each supported language, and which build
// profile that language maps to. cmd/platform-factory-plugin-
// languages/main.go only speaks the sdk/plugin wire protocol (decode
// params, call in, encode result); the actual per-language decision
// table lives here, testable without a plugin handshake at all.
package languageplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FreezeSteps returns the package-manager commands that pin language's
// dependencies at root, and the build profile language maps to. Which
// lockfile/command set applies for node, java, and python depends on
// what's already present at root (see the "exists" checks below) -
// exactly the same evidence-based selection `pf freeze`'s own built-in
// adapters use, so a lockfile that's already present is trusted over
// generating a new one from scratch.
func FreezeSteps(language, root string) (steps [][]string, err error) {
	exists := func(name string) bool {
		info, statErr := os.Stat(filepath.Join(root, name))
		return statErr == nil && info.Mode().IsRegular()
	}
	switch strings.ToLower(language) {
	case "go", "golang":
		return [][]string{{"go", "mod", "tidy"}, {"go", "mod", "vendor"}}, nil
	case "node", "nodejs", "javascript", "typescript":
		if exists("package-lock.json") || exists("npm-shrinkwrap.json") {
			return [][]string{{"npm", "ci", "--ignore-scripts"}}, nil
		}
		return [][]string{{"npm", "install", "--package-lock-only", "--ignore-scripts"}, {"npm", "ci", "--ignore-scripts"}}, nil
	case "python":
		requirements := "requirements.lock"
		if !exists(requirements) {
			requirements = "requirements.txt"
		}
		if !exists(requirements) {
			return nil, fmt.Errorf("python requires requirements.lock or requirements.txt")
		}
		return [][]string{
			{"python", "-m", "pip", "install", "--requirement", requirements, "--target", ".platform-factory/deps/python"},
			{"python", "-m", "pip", "freeze", "--path", ".platform-factory/deps/python"},
		}, nil
	case "java":
		switch {
		case exists("mvnw"):
			return [][]string{{"./mvnw", "-B", "dependency:go-offline"}}, nil
		case exists("gradlew"):
			return [][]string{{"./gradlew", "dependencies", "--write-locks"}}, nil
		case exists("pom.xml"):
			return [][]string{{"mvn", "-B", "dependency:go-offline"}}, nil
		default:
			return nil, fmt.Errorf("java requires Maven or Gradle project files")
		}
	case "dotnet", "csharp", "fsharp":
		return [][]string{{"dotnet", "restore", "--use-lock-file"}}, nil
	case "rust":
		return [][]string{{"cargo", "generate-lockfile"}, {"cargo", "fetch", "--locked"}}, nil
	case "ruby":
		return [][]string{{"bundle", "lock"}, {"bundle", "cache", "--all"}}, nil
	case "php":
		return [][]string{{"composer", "install", "--no-dev", "--prefer-dist", "--no-interaction"}}, nil
	default:
		return nil, fmt.Errorf("unsupported language %q", language)
	}
}

// Profile maps language to the build profile `pf build` uses to choose
// how to package it. Go and Rust deliberately map to "static": they
// produce ELF executables, and oci's own ELF detection picks
// static/glibc/musl from the actual binary rather than from the
// language name.
func Profile(language string) string {
	switch strings.ToLower(language) {
	case "go", "golang", "rust":
		return "static"
	case "node", "nodejs", "javascript", "typescript":
		return "node"
	case "dotnet", "csharp", "fsharp":
		return "dotnet"
	default:
		return strings.ToLower(language)
	}
}

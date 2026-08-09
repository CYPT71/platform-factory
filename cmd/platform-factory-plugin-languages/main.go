// platform-factory-plugin-languages is the official language adapter plugin. It is
// deliberately out of process and uses only the public plugin SDK on its wire
// boundary.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/detect"
	plugin "github.com/CYPT71/secure-oci-base/sdk/plugin"
)

func main() {
	server := plugin.NewServer("platform-factory-languages", "v1.0.0")
	server.Handle(plugin.CapabilityDetect, handleDetect)
	server.Handle(plugin.CapabilityFreeze, handleFreeze)
	server.Handle(plugin.CapabilityPlan, handlePlan)
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "platform-factory-plugin-languages:", err)
		os.Exit(1)
	}
}

func handleDetect(_ context.Context, raw json.RawMessage) (any, error) {
	var params plugin.DetectParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if params.Path == "" {
		return nil, errors.New("params.path is required")
	}
	result, err := detect.Path(params.Path)
	if err != nil {
		return nil, err
	}
	return plugin.DetectResult{
		Kind: result.Kind, Profile: result.Profile, Evidence: result.Evidence,
	}, nil
}

func handleFreeze(_ context.Context, raw json.RawMessage) (any, error) {
	var params plugin.FreezeParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if params.Root == "" {
		return nil, errors.New("params.root is required")
	}
	exists := func(name string) bool {
		info, err := os.Stat(filepath.Join(params.Root, name))
		return err == nil && info.Mode().IsRegular()
	}
	var steps [][]string
	switch strings.ToLower(params.Language) {
	case "go", "golang":
		steps = [][]string{{"go", "mod", "tidy"}, {"go", "mod", "vendor"}}
	case "node", "nodejs", "javascript", "typescript":
		if exists("package-lock.json") || exists("npm-shrinkwrap.json") {
			steps = [][]string{{"npm", "ci", "--ignore-scripts"}}
		} else {
			steps = [][]string{{"npm", "install", "--package-lock-only", "--ignore-scripts"}, {"npm", "ci", "--ignore-scripts"}}
		}
	case "python":
		requirements := "requirements.lock"
		if !exists(requirements) {
			requirements = "requirements.txt"
		}
		if !exists(requirements) {
			return nil, errors.New("python requires requirements.lock or requirements.txt")
		}
		steps = [][]string{
			{"python", "-m", "pip", "install", "--requirement", requirements, "--target", ".platform-factory/deps/python"},
			{"python", "-m", "pip", "freeze", "--path", ".platform-factory/deps/python"},
		}
	case "java":
		switch {
		case exists("mvnw"):
			steps = [][]string{{"./mvnw", "-B", "dependency:go-offline"}}
		case exists("gradlew"):
			steps = [][]string{{"./gradlew", "dependencies", "--write-locks"}}
		case exists("pom.xml"):
			steps = [][]string{{"mvn", "-B", "dependency:go-offline"}}
		default:
			return nil, errors.New("java requires Maven or Gradle project files")
		}
	case "dotnet", "csharp", "fsharp":
		steps = [][]string{{"dotnet", "restore", "--use-lock-file"}}
	case "rust":
		steps = [][]string{{"cargo", "generate-lockfile"}, {"cargo", "fetch", "--locked"}}
	case "ruby":
		steps = [][]string{{"bundle", "lock"}, {"bundle", "cache", "--all"}}
	case "php":
		steps = [][]string{{"composer", "install", "--no-dev", "--prefer-dist", "--no-interaction"}}
	default:
		return nil, fmt.Errorf("unsupported language %q", params.Language)
	}
	return plugin.FreezeResult{Steps: steps, Profile: profile(params.Language)}, nil
}

func handlePlan(_ context.Context, raw json.RawMessage) (any, error) {
	var params plugin.PlanParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	if params.Language == "" {
		return nil, errors.New("params.language is required")
	}
	return plugin.PlanResult{Notes: []string{
		"official adapter selected for " + strings.ToLower(params.Language),
		"dependency commands are returned to the host and never executed by the plugin",
	}}, nil
}

func profile(language string) string {
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

// platform-factory-plugin-demo is the minimal reference language extension. It
// imports only the public SDK and implements detect, freeze and plan.
package main

import (
	"context"
	"fmt"
	"os"

	plugin "github.com/CYPT71/platform-factory/sdk/plugin"
)

type extension struct{}

func (extension) Detect(_ context.Context, params plugin.DetectParams) (plugin.DetectResult, error) {
	if params.Path == "" {
		return plugin.DetectResult{}, fmt.Errorf("params.path is required")
	}
	if _, err := os.Stat(params.Path); err != nil {
		return plugin.DetectResult{}, fmt.Errorf("inspect project: %w", err)
	}
	return plugin.DetectResult{Kind: "example", Profile: "static", Evidence: []string{"reference-plugin"}}, nil
}

func (extension) Freeze(_ context.Context, params plugin.FreezeParams) (plugin.FreezeResult, error) {
	if params.Language == "" || params.Root == "" {
		return plugin.FreezeResult{}, fmt.Errorf("params.language and params.root are required")
	}
	return plugin.FreezeResult{Steps: [][]string{{"example-package-manager", "freeze"}}, Profile: "static"}, nil
}

func (extension) Plan(_ context.Context, params plugin.PlanParams) (plugin.PlanResult, error) {
	if params.Language == "" || params.Root == "" {
		return plugin.PlanResult{}, fmt.Errorf("params.language and params.root are required")
	}
	return plugin.PlanResult{Notes: []string{"reference extension selected"}}, nil
}

func main() {
	runtime, err := plugin.NewRuntime("platform-factory-plugin-demo", "v1.0.0", extension{})
	if err == nil {
		err = runtime.Serve(context.Background(), os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "platform-factory-plugin-demo:", err)
		os.Exit(1)
	}
}

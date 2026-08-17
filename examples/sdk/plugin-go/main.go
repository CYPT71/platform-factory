package main

import (
	"context"
	"fmt"
	"os"

	"github.com/CYPT71/platform-factory/sdk/plugin"
)

type language struct{}

func (language) Detect(context.Context, plugin.DetectParams) (plugin.DetectResult, error) {
	return plugin.DetectResult{Kind: "my-language", Profile: "static"}, nil
}
func (language) Freeze(context.Context, plugin.FreezeParams) (plugin.FreezeResult, error) {
	return plugin.FreezeResult{Steps: [][]string{{"my-package-manager", "freeze"}}, Profile: "static"}, nil
}
func (language) Plan(context.Context, plugin.PlanParams) (plugin.PlanResult, error) {
	return plugin.PlanResult{Notes: []string{"my-language extension selected"}}, nil
}

func main() {
	runtime, err := plugin.NewRuntime("my-language", "1.0.0", language{})
	if err == nil {
		err = runtime.Serve(context.Background(), os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

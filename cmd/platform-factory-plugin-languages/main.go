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
	"strings"

	"github.com/CYPT71/platform-factory/internal/app/languageplugin"
	"github.com/CYPT71/platform-factory/internal/detect"
	plugin "github.com/CYPT71/platform-factory/sdk/plugin"
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
	steps, err := languageplugin.FreezeSteps(params.Language, params.Root)
	if err != nil {
		return nil, err
	}
	return plugin.FreezeResult{Steps: steps, Profile: languageplugin.Profile(params.Language)}, nil
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

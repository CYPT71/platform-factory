// Command secure-oci-zig-plugin is the third-party proof for the plugin
// boundary: a separate Go module that adds support for a language the
// secure-oci core knows nothing about ("zig"), importing only the
// public sdk/plugin SDK. The host discovers it through a signed
// manifest and consults it for detect and freeze without recompiling
// secure-oci.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	plugin "github.com/CYPT71/secure-oci-base/sdk/plugin"
)

func handleDetect(_ context.Context, raw json.RawMessage) (any, error) {
	var params plugin.DetectParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	if params.Path == "" {
		return nil, errors.New("params.path is required")
	}
	if info, err := os.Stat(filepath.Join(params.Path, "build.zig")); err == nil && info.Mode().IsRegular() {
		return plugin.DetectResult{Kind: "zig", Profile: "static", Evidence: []string{"build.zig"}}, nil
	}
	return plugin.DetectResult{Kind: "unknown"}, nil
}

func handleFreeze(_ context.Context, raw json.RawMessage) (any, error) {
	var params plugin.FreezeParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	if params.Language != "zig" {
		return nil, fmt.Errorf("unsupported language %q", params.Language)
	}
	return plugin.FreezeResult{
		Steps:   [][]string{{"zig", "build", "--fetch"}},
		Profile: "static",
	}, nil
}

func handlePlan(_ context.Context, raw json.RawMessage) (any, error) {
	var params plugin.PlanParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	return plugin.PlanResult{Notes: []string{"zig dependencies are fetched with zig build --fetch"}}, nil
}

func main() {
	server := plugin.NewServer("zig-adapter", "v0.1.0")
	server.Handle(plugin.CapabilityDetect, handleDetect)
	server.Handle(plugin.CapabilityFreeze, handleFreeze)
	server.Handle(plugin.CapabilityPlan, handlePlan)
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "platform-factory-zig-plugin:", err)
		os.Exit(1)
	}
}

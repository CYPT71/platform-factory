package main

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/plugin"
	api "github.com/CYPT71/secure-oci-base/sdk/plugin"
)

// pluginOptions carries the plugin trust flags shared by every command
// that consults plugins.
type pluginOptions struct {
	dir              string
	keyFiles         repeatedFlag
	allowUnverified  bool
	allowUnsandboxed bool
}

func registerPluginFlags(flags *flag.FlagSet) *pluginOptions {
	options := &pluginOptions{dir: os.Getenv("PLATFORM_FACTORY_PLUGIN_DIR")}
	flags.StringVar(&options.dir, "plugin-dir", options.dir,
		"directory of installed plugins, one subdirectory with plugin.json each")
	flags.Var(&options.keyFiles, "plugin-key", "PEM Ed25519 public key trusted for plugin manifests; repeatable")
	flags.BoolVar(&options.allowUnverified, "allow-unverified-plugin", false,
		"accept plugins with unsigned manifests; executable digest pins are still enforced")
	flags.BoolVar(&options.allowUnsandboxed, "allow-unsandboxed-plugin", false,
		"explicitly allow plugin execution when the host cannot enforce its sandbox")
	return options
}

// pluginClient is the surface pluginHost needs from a started plugin;
// tests substitute stubs.
type pluginClient interface {
	Hello() plugin.HelloResult
	HasCapability(capability string) bool
	Call(ctx context.Context, method string, params, result any) error
	Close() error
}

// pluginHost holds every verified, started plugin for one command run.
type pluginHost struct {
	clients []pluginClient
}

// start discovers, verifies and starts every plugin under the
// configured directory. No directory means no plugins and no error.
func (options *pluginOptions) start(ctx context.Context) (*pluginHost, error) {
	host := &pluginHost{}
	if options == nil || options.dir == "" {
		return host, nil
	}
	var keys []ed25519.PublicKey
	for _, filename := range options.keyFiles {
		key, err := plugin.LoadPublicKey(filename)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	policy := plugin.TrustPolicy{
		Keys:                      keys,
		AllowUnsigned:             options.allowUnverified,
		AllowUnsandboxedExecution: options.allowUnsandboxed,
	}
	discovered, err := plugin.Discover(options.dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range discovered {
		client, err := plugin.VerifyAndStart(ctx, entry.Dir, entry.Manifest, policy)
		if err != nil {
			host.Close()
			return nil, err
		}
		host.clients = append(host.clients, client)
	}
	return host, nil
}

func (host *pluginHost) Close() {
	for _, client := range host.clients {
		_ = client.Close()
	}
}

// detect consults every plugin with the detect capability, in
// discovery order, and returns the first non-unknown classification.
func (host *pluginHost) detect(ctx context.Context, path string) (api.DetectResult, string, bool) {
	for _, client := range host.clients {
		if !client.HasCapability(api.CapabilityDetect) {
			continue
		}
		var result api.DetectResult
		if err := client.Call(ctx, "v1."+api.CapabilityDetect, api.DetectParams{Path: path}, &result); err != nil {
			continue
		}
		if result.Kind != "" && result.Kind != "unknown" {
			return result, client.Hello().Name, true
		}
	}
	return api.DetectResult{}, "", false
}

// freeze asks the first plugin with the freeze capability that accepts
// the language for its freeze commands, and validates every returned
// argument with the same rules as a configured freeze_command.
func (host *pluginHost) freeze(ctx context.Context, language, root string) ([]freezeStep, string, error) {
	for _, client := range host.clients {
		if !client.HasCapability(api.CapabilityFreeze) {
			continue
		}
		var result api.FreezeResult
		if err := client.Call(ctx, "v1."+api.CapabilityFreeze, api.FreezeParams{Language: language, Root: root}, &result); err != nil {
			continue
		}
		steps := make([]freezeStep, 0, len(result.Steps))
		for _, argv := range result.Steps {
			if len(argv) == 0 {
				return nil, "", fmt.Errorf("plugin %s returned an empty freeze command", client.Hello().Name)
			}
			for _, argument := range argv {
				if argument == "" || strings.ContainsRune(argument, 0) {
					return nil, "", fmt.Errorf("plugin %s returned an invalid freeze argument", client.Hello().Name)
				}
			}
			steps = append(steps, freezeStep{args: append([]string(nil), argv...)})
		}
		return steps, client.Hello().Name, nil
	}
	return nil, "", errNoPluginFreeze
}

// planNotes collects advisory notes from every plugin with the plan
// capability. Notes never change what the host executes.
func (host *pluginHost) planNotes(ctx context.Context, language, root string) []string {
	var notes []string
	for _, client := range host.clients {
		if !client.HasCapability(api.CapabilityPlan) {
			continue
		}
		var result api.PlanResult
		if err := client.Call(ctx, "v1."+api.CapabilityPlan, api.PlanParams{Language: language, Root: root}, &result); err != nil {
			continue
		}
		for _, note := range result.Notes {
			if note != "" {
				notes = append(notes, client.Hello().Name+": "+note)
			}
		}
	}
	return notes
}

var errNoPluginFreeze = fmt.Errorf("no plugin provides a freeze adapter")

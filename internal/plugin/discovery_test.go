package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
)

func TestDiscoverAndRegister(t *testing.T) {
	// Create a temporary directory with test plugins
	tmpDir, err := os.MkdirTemp("", "plugin-discovery-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test plugin directory with manifest
	pluginDir := filepath.Join(tmpDir, "test-plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("failed to create plugin dir: %v", err)
	}

	manifestContent := `{
		"api_version": "platform-factory.dev/plugin-manifest/v1",
		"name": "test-plugin",
		"version": "1.0.0",
		"family": "runtime",
		"capabilities": ["runtime.create", "runtime.stop"],
		"permissions": {"network": [], "filesystem": [], "secrets": []},
		"executable": "test-plugin",
		"digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	}`
	manifestPath := filepath.Join(pluginDir, ManifestFileName)
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	// Create the executable (required by manifest validation)
	execPath := filepath.Join(pluginDir, "test-plugin")
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\necho test"), 0755); err != nil {
		t.Fatalf("failed to write executable: %v", err)
	}

	// Test DiscoverAndRegister
	registry, err := DiscoverAndRegister(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverAndRegister failed: %v", err)
	}
	candidates, err := registry.Candidates(context.Background(), appmigration.CapabilityRequirement{Capability: "runtime.create"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates=%+v", candidates)
	}
	if candidates[0].Evidence.Available || candidates[0].Evidence.Verified || candidates[0].Evidence.Negotiated {
		t.Fatalf("discovery made an unverified digest available: %+v", candidates[0].Evidence)
	}

	// Check that the plugin was registered
	if !registry.DeclaredHasCapability("runtime.create") {
		t.Fatal("expected registry to have runtime.create capability")
	}
	if !registry.DeclaredHasCapability("runtime.stop") {
		t.Fatal("expected registry to have runtime.stop capability")
	}

	plugins := registry.DeclaredPluginsWithCapability("runtime.create")
	if len(plugins) != 1 || plugins[0] != "test-plugin" {
		t.Fatalf("expected [test-plugin], got %v", plugins)
	}

	runtimePlugins := registry.GetPluginsByFamily("runtime")
	if len(runtimePlugins) != 1 || runtimePlugins[0] != "test-plugin" {
		t.Fatalf("expected [test-plugin] for runtime family, got %v", runtimePlugins)
	}
}

func TestDiscoverAndRegisterGlobalPublishesDeclarationsOnly(t *testing.T) {
	original := globalRegistry
	globalRegistry = NewRegistry()
	defer func() { globalRegistry = original }()

	root := t.TempDir()
	pluginDir := filepath.Join(root, "global-plugin")
	if err := os.Mkdir(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"api_version":"platform-factory.dev/plugin-manifest/v1",
		"name":"global-plugin","version":"1.0.0","family":"runtime",
		"capabilities":["runtime.observe"],
		"permissions":{"network":[],"filesystem":[],"secrets":[]},
		"executable":"plugin",
		"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"
	}`
	if err := os.WriteFile(filepath.Join(pluginDir, ManifestFileName), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin"), []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := DiscoverAndRegisterGlobal(root); err != nil {
		t.Fatal(err)
	}
	if GlobalRegistry() != globalRegistry {
		t.Fatal("GlobalRegistry returned a different registry")
	}
	if !globalRegistry.DeclaredHasCapability("runtime.observe") {
		t.Fatal("global discovery did not publish declaration")
	}
	if globalRegistry.HasCapability("runtime.observe") {
		t.Fatal("global discovery bypassed verification")
	}
	if err := DiscoverAndRegisterGlobal(filepath.Join(root, "missing")); err == nil {
		t.Fatal("global discovery ignored unreadable root")
	}
}

func TestDiscoverAndRegisterMultiplePlugins(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "plugin-discovery-multi-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create multiple test plugins
	plugins := []struct {
		name         string
		family       string
		capabilities []string
	}{
		{"plugin-a", "runtime", []string{"runtime.create", "runtime.stop"}},
		{"plugin-b", "runtime", []string{"runtime.create", "runtime.logs"}},
		{"plugin-c", "deployment", []string{"deployment.apply", "deployment.observe"}},
	}

	for _, p := range plugins {
		pluginDir := filepath.Join(tmpDir, p.name)
		if err := os.MkdirAll(pluginDir, 0755); err != nil {
			t.Fatalf("failed to create plugin dir %s: %v", p.name, err)
		}

		capabilitiesJSON := ""
		for i, cap := range p.capabilities {
			if i > 0 {
				capabilitiesJSON += ","
			}
			capabilitiesJSON += `"` + cap + `"`
		}

		manifestContent := `{
			"api_version": "platform-factory.dev/plugin-manifest/v1",
			"name": "` + p.name + `",
			"version": "1.0.0",
			"family": "` + p.family + `",
			"capabilities": [` + capabilitiesJSON + `],
			"permissions": {"network": [], "filesystem": [], "secrets": []},
			"executable": "` + p.name + `",
			"digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		}`
		manifestPath := filepath.Join(pluginDir, ManifestFileName)
		if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
			t.Fatalf("failed to write manifest for %s: %v", p.name, err)
		}

		execPath := filepath.Join(pluginDir, p.name)
		if err := os.WriteFile(execPath, []byte("#!/bin/sh\necho test"), 0755); err != nil {
			t.Fatalf("failed to write executable for %s: %v", p.name, err)
		}
	}

	registry, err := DiscoverAndRegister(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverAndRegister failed: %v", err)
	}

	// Check all plugins were registered
	allPlugins := registry.GetAllPlugins()
	if len(allPlugins) != 3 {
		t.Fatalf("expected 3 plugins, got %d", len(allPlugins))
	}

	// Check runtime.create is provided by 2 plugins
	runtimeCreatePlugins := registry.DeclaredPluginsWithCapability("runtime.create")
	if len(runtimeCreatePlugins) != 2 {
		t.Fatalf("expected 2 plugins with runtime.create, got %d", len(runtimeCreatePlugins))
	}

	// Check deployment.apply is provided by 1 plugin
	deploymentApplyPlugins := registry.DeclaredPluginsWithCapability("deployment.apply")
	if len(deploymentApplyPlugins) != 1 || deploymentApplyPlugins[0] != "plugin-c" {
		t.Fatalf("expected [plugin-c] for deployment.apply, got %v", deploymentApplyPlugins)
	}

	// Check family indexing
	runtimePlugins := registry.GetPluginsByFamily("runtime")
	if len(runtimePlugins) != 2 {
		t.Fatalf("expected 2 runtime plugins, got %d", len(runtimePlugins))
	}

	deploymentPlugins := registry.GetPluginsByFamily("deployment")
	if len(deploymentPlugins) != 1 || deploymentPlugins[0] != "plugin-c" {
		t.Fatalf("expected [plugin-c] for deployment family, got %v", deploymentPlugins)
	}
}

func TestDiscoverAndRegisterEmptyDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "plugin-discovery-empty-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Test with empty directory (no plugins)
	registry, err := DiscoverAndRegister(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverAndRegister failed on empty dir: %v", err)
	}

	if len(registry.GetAllPlugins()) != 0 {
		t.Fatalf("expected 0 plugins in empty directory, got %d", len(registry.GetAllPlugins()))
	}

	if registry.HasCapability("runtime.create") {
		t.Fatal("expected no capabilities in empty registry")
	}
}

func TestDiscoverAndRegisterInvalidManifest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "plugin-discovery-invalid-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a plugin directory with invalid manifest
	pluginDir := filepath.Join(tmpDir, "invalid-plugin")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("failed to create plugin dir: %v", err)
	}

	// Write invalid manifest (missing required fields)
	manifestContent := `{
		"api_version": "platform-factory.dev/plugin-manifest/v1",
		"name": ""
	}`
	manifestPath := filepath.Join(pluginDir, ManifestFileName)
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	// DiscoverAndRegister should fail due to validation error
	_, err = DiscoverAndRegister(tmpDir)
	if err == nil {
		t.Fatal("expected error for invalid manifest")
	}
}

func TestDiscoverAndRegisterDuplicatePluginNames(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "plugin-discovery-duplicate-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create two plugins with the same name (in direct subdirectories)
	for i := 0; i < 2; i++ {
		pluginDir := filepath.Join(tmpDir, "duplicate-"+string(rune('a'+i)))
		if err := os.MkdirAll(pluginDir, 0755); err != nil {
			t.Fatalf("failed to create plugin dir: %v", err)
		}

		manifestContent := `{
			"api_version": "platform-factory.dev/plugin-manifest/v1",
			"name": "duplicate-plugin",
			"version": "1.0.0",
			"family": "runtime",
			"capabilities": ["runtime.create"],
			"permissions": {"network": [], "filesystem": [], "secrets": []},
			"executable": "duplicate-plugin",
			"digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		}`
		manifestPath := filepath.Join(pluginDir, ManifestFileName)
		if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
			t.Fatalf("failed to write manifest: %v", err)
		}

		execPath := filepath.Join(pluginDir, "duplicate-plugin")
		if err := os.WriteFile(execPath, []byte("#!/bin/sh\necho test"), 0755); err != nil {
			t.Fatalf("failed to write executable: %v", err)
		}
	}

	// DiscoverAndRegister should fail due to duplicate plugin names
	_, err = DiscoverAndRegister(tmpDir)
	if err == nil {
		t.Fatal("expected error for duplicate plugin names")
	}
}

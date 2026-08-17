package plugin

import (
	"context"
	"fmt"
	"sync"
	"testing"

	appmigration "github.com/CYPT71/platform-factory/internal/app/migration"
)

func publishTestPlugin(t *testing.T, r *Registry, manifest Manifest) *Client {
	t.Helper()
	r.registerDiscovered(manifest)
	client := &Client{}
	if err := r.publishAvailable(manifest, client); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewRegistry()

	manifest := Manifest{
		APIVersion: ManifestAPIVersion,
		Name:       "test-plugin",
		Version:    "1.0.0",

		Family:       PluginFamilyRuntime,
		Capabilities: []string{"runtime.create", "runtime.stop"},
	}

	// Register the manifest
	r.Register(manifest)

	// Check GetManifest
	got, ok := r.GetManifest("test-plugin")
	if !ok {
		t.Fatal("expected to find test-plugin")
	}
	if got.Name != "test-plugin" {
		t.Fatalf("expected name test-plugin, got %q", got.Name)
	}

	// Check HasCapability
	if !r.DeclaredHasCapability("runtime.create") {
		t.Fatal("expected HasCapability(runtime.create) to be true")
	}
	if !r.DeclaredHasCapability("runtime.stop") {
		t.Fatal("expected HasCapability(runtime.stop) to be true")
	}
	if r.HasCapability("runtime.logs") {
		t.Fatal("expected HasCapability(runtime.logs) to be false")
	}

	// Check GetPluginsWithCapability
	plugins := r.DeclaredPluginsWithCapability("runtime.create")
	if len(plugins) != 1 || plugins[0] != "test-plugin" {
		t.Fatalf("expected [test-plugin], got %v", plugins)
	}

	// Check GetPluginsByFamily
	plugins = r.GetPluginsByFamily("runtime")
	if len(plugins) != 1 || plugins[0] != "test-plugin" {
		t.Fatalf("expected [test-plugin], got %v", plugins)
	}
}

func TestRegistryRegisterIsIdempotent(t *testing.T) {
	r := NewRegistry()
	manifest := Manifest{Name: "same", Family: PluginFamilyRuntime, Capabilities: []string{"runtime.create"}}
	r.Register(manifest)
	r.Register(manifest)
	if got := r.DeclaredPluginsWithCapability("runtime.create"); len(got) != 1 || got[0] != "same" {
		t.Fatalf("capability index duplicated: %v", got)
	}
	if got := r.GetPluginsByFamily("runtime"); len(got) != 1 || got[0] != "same" {
		t.Fatalf("family index duplicated: %v", got)
	}
	conflict := manifest
	conflict.Capabilities = []string{"runtime.delete"}
	r.Register(conflict)
	if r.DeclaredHasCapability("runtime.delete") {
		t.Fatal("conflicting registration replaced identity")
	}
}

func TestRegistryManifestSnapshotsCannotBeMutatedByCallers(t *testing.T) {
	r := NewRegistry()
	manifest := Manifest{Name: "snapshot", Version: "v1", Digest: "sha256:" + string(make([]byte, 64)), Family: PluginFamilyDeployment,
		Capabilities: []string{"deployment.apply"}, Platforms: []string{"linux/amd64"},
		Permissions: PluginPermissions{Network: []string{"declared-egress"}, Filesystem: []string{"workspace"}, Secrets: []string{"token-ref"}},
		Signature:   &ManifestSignature{Algorithm: "ed25519", KeyID: "key", Value: "signature"}}
	r.registerDiscovered(manifest)
	client := &Client{}
	if err := r.publishAvailable(manifest, client); err != nil {
		t.Fatal(err)
	}
	manifest.Capabilities[0] = "deployment.delete"
	manifest.Platforms[0] = "other/platform"
	manifest.Permissions.Network[0] = "all-egress"
	manifest.Permissions.Filesystem[0] = "/"
	manifest.Permissions.Secrets[0] = "all-secrets"
	manifest.Signature.KeyID = "attacker"
	got, ok := r.GetManifest("snapshot")
	if !ok || got.Capabilities[0] != "deployment.apply" || got.Platforms[0] != "linux/amd64" || got.Permissions.Network[0] != "declared-egress" || got.Signature.KeyID != "key" {
		t.Fatalf("caller mutated registry ingress snapshot: %+v", got)
	}
	got.Capabilities[0] = "deployment.delete"
	got.Permissions.Network[0] = "all-egress"
	again, _ := r.GetManifest("snapshot")
	if again.Capabilities[0] != "deployment.apply" || again.Permissions.Network[0] != "declared-egress" {
		t.Fatalf("caller mutated registry egress snapshot: %+v", again)
	}
	candidates, err := r.Candidates(context.Background(), appmigration.CapabilityRequirement{Capability: "deployment.apply"})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	if candidates[0].Digest != "sha256:"+string(make([]byte, 64)) || len(candidates[0].Permissions.Network) != 0 || len(candidates[0].Permissions.Filesystem) != 0 || len(candidates[0].Permissions.Secrets) != 0 {
		t.Fatalf("candidate used mutable or merely-declared permissions: %+v", candidates[0])
	}
}

func TestRegistryConcurrentEgressMutationDoesNotRaceOrChangeState(t *testing.T) {
	r := NewRegistry()
	manifest := Manifest{Name: "concurrent", Capabilities: []string{"deployment.observe"}, Platforms: []string{"linux/amd64"}}
	r.Register(manifest)
	var group sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 100; iteration++ {
				got, ok := r.GetManifest("concurrent")
				if !ok {
					return
				}
				got.Capabilities[0] = "deployment.delete"
				got.Platforms[0] = "other/platform"
			}
		}()
	}
	group.Wait()
	got, _ := r.GetManifest("concurrent")
	if got.Capabilities[0] != "deployment.observe" || got.Platforms[0] != "linux/amd64" {
		t.Fatalf("concurrent caller mutation escaped snapshot: %+v", got)
	}
}

func TestRegistryMultiplePlugins(t *testing.T) {
	r := NewRegistry()

	manifest1 := Manifest{
		APIVersion: ManifestAPIVersion,
		Name:       "plugin-a",
		Version:    "1.0.0",

		Family:       PluginFamilyRuntime,
		Capabilities: []string{"runtime.create", "runtime.stop"},
	}

	manifest2 := Manifest{
		APIVersion: ManifestAPIVersion,
		Name:       "plugin-b",
		Version:    "1.0.0",

		Family:       PluginFamilyRuntime,
		Capabilities: []string{"runtime.create", "runtime.logs"},
	}

	manifest3 := Manifest{
		APIVersion: ManifestAPIVersion,
		Name:       "plugin-c",
		Version:    "1.0.0",

		Family:       PluginFamilyDeployment,
		Capabilities: []string{"deployment.apply", "deployment.observe"},
	}

	r.Register(manifest1)
	r.Register(manifest2)
	r.Register(manifest3)

	// Check GetAllPlugins
	plugins := r.GetAllPlugins()
	if len(plugins) != 3 {
		t.Fatalf("expected 3 plugins, got %d", len(plugins))
	}

	// Check GetPluginsWithCapability for runtime.create (2 plugins)
	plugins = r.DeclaredPluginsWithCapability("runtime.create")
	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugins for runtime.create, got %d", len(plugins))
	}

	// Check GetPluginsWithCapability for deployment.apply (1 plugin)
	plugins = r.DeclaredPluginsWithCapability("deployment.apply")
	if len(plugins) != 1 || plugins[0] != "plugin-c" {
		t.Fatalf("expected [plugin-c], got %v", plugins)
	}

	// Check GetPluginsByFamily
	runtimePlugins := r.GetPluginsByFamily("runtime")
	if len(runtimePlugins) != 2 {
		t.Fatalf("expected 2 runtime plugins, got %d", len(runtimePlugins))
	}

	deploymentPlugins := r.GetPluginsByFamily("deployment")
	if len(deploymentPlugins) != 1 || deploymentPlugins[0] != "plugin-c" {
		t.Fatalf("expected [plugin-c], got %v", deploymentPlugins)
	}

	// Check GetAllCapabilities
	caps := r.GetAllCapabilities()
	if len(caps) != 5 {
		t.Fatalf("expected 5 unique capabilities, got %d: %v", len(caps), caps)
	}
}

func TestRegistryUnregister(t *testing.T) {
	r := NewRegistry()

	manifest := Manifest{
		APIVersion: ManifestAPIVersion,
		Name:       "test-plugin",
		Version:    "1.0.0",

		Family:       PluginFamilyRuntime,
		Capabilities: []string{"runtime.create", "runtime.stop"},
	}

	r.Register(manifest)

	// Verify it's registered
	if !r.DeclaredHasCapability("runtime.create") {
		t.Fatal("expected runtime.create to be registered")
	}

	// Unregister
	r.Unregister("test-plugin")

	// Verify it's removed
	if r.HasCapability("runtime.create") {
		t.Fatal("expected runtime.create to be removed after unregister")
	}

	_, ok := r.GetManifest("test-plugin")
	if ok {
		t.Fatal("expected test-plugin to be removed after unregister")
	}
}

func TestRegistryGetPluginWithCapability(t *testing.T) {
	r := NewRegistry()

	manifest := Manifest{
		APIVersion: ManifestAPIVersion,
		Name:       "kubevirt-plugin",
		Version:    "1.2.0",

		Family:       PluginFamilyDeployment,
		Capabilities: []string{"deployment.plan", "deployment.apply", "deployment.observe"},
	}

	publishTestPlugin(t, r, manifest)

	// Get plugin with deployment.apply capability
	plugin, ok := r.GetPluginWithCapability("deployment.apply")
	if !ok {
		t.Fatal("expected to find plugin with deployment.apply")
	}
	if plugin.Name != "kubevirt-plugin" {
		t.Fatalf("expected kubevirt-plugin, got %q", plugin.Name)
	}
	if len(plugin.Capabilities) != 3 {
		t.Fatalf("expected 3 capabilities, got %d", len(plugin.Capabilities))
	}

	// Try non-existent capability
	_, ok = r.GetPluginWithCapability("deployment.delete")
	if ok {
		t.Fatal("expected no plugin with deployment.delete")
	}
}

func TestRegistryRequireCapability(t *testing.T) {
	r := NewRegistry()

	manifest := Manifest{
		APIVersion: ManifestAPIVersion,
		Name:       "test-plugin",
		Version:    "1.0.0",

		Family:       PluginFamilyRuntime,
		Capabilities: []string{"runtime.create"},
	}

	publishTestPlugin(t, r, manifest)

	// Require existing capability - should succeed
	err := r.RequireCapability("runtime.create")
	if err != nil {
		t.Fatalf("expected no error for runtime.create, got: %v", err)
	}

	// Require non-existent capability - should fail
	err = r.RequireCapability("runtime.stop")
	if err == nil {
		t.Fatal("expected error for runtime.stop")
	}
	if !IsCapabilityError(err) {
		t.Fatalf("expected CapabilityError, got %T", err)
	}
}

func TestRegistryCapabilityError(t *testing.T) {
	err := &CapabilityError{
		Capability: "test.cap",
		Message:    "test message",
	}

	expected := "test message: test.cap"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}

	if !IsCapabilityError(err) {
		t.Fatal("expected IsCapabilityError to return true")
	}

	// Test with non-CapabilityError
	otherErr := fmt.Errorf("test error")
	if IsCapabilityError(otherErr) {
		t.Fatal("expected IsCapabilityError to return false for non-CapabilityError")
	}
}

func TestRegistryPublishAvailableRequiresExactDiscoveredManifest(t *testing.T) {
	r := NewRegistry()
	manifest := Manifest{Name: "candidate", Version: "v1", Digest: "sha256:" + string(make([]byte, 64)), Capabilities: []string{"migration.apply"}}
	r.registerDiscovered(manifest)
	mismatch := manifest
	mismatch.Capabilities = []string{"migration.delete"}
	if err := r.publishAvailable(mismatch, &Client{}); err == nil {
		t.Fatal("expected mismatched verification evidence to be rejected")
	}
	if r.states[manifest.Name].verified || r.states[manifest.Name].client != nil {
		t.Fatal("mismatched evidence published verification state")
	}
}

func TestRegistryPublishAvailableFailsClosedWithoutLifecycleEvidence(t *testing.T) {
	manifest := Manifest{Name: "candidate", Capabilities: []string{"migration.apply"}}
	tests := []struct {
		name    string
		prepare func(*Registry)
		client  *Client
	}{
		{name: "not registered", prepare: func(*Registry) {}, client: &Client{}},
		{name: "declared but not discovered", prepare: func(r *Registry) { r.Register(manifest) }, client: &Client{}},
		{name: "dead client", prepare: func(r *Registry) { r.registerDiscovered(manifest) }, client: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			tt.prepare(r)
			if err := r.publishAvailable(manifest, tt.client); err == nil {
				t.Fatal("availability published without complete evidence")
			}
			if r.HasCapability("migration.apply") {
				t.Fatal("incomplete lifecycle became dispatchable")
			}
		})
	}
}

func TestGlobalRegistryFunctions(t *testing.T) {
	// Clear global registry (for test isolation)
	// Note: This is safe in tests as they run in parallel with their own state

	manifest := Manifest{
		APIVersion: ManifestAPIVersion,
		Name:       "global-test-plugin",
		Version:    "1.0.0",

		Family:       PluginFamilyRuntime,
		Capabilities: []string{"runtime.create"},
	}

	RegisterGlobal(manifest)
	defer UnregisterGlobal("global-test-plugin")

	// A declaration alone is deliberately not dispatchable.
	if HasGlobalCapability("runtime.create") {
		t.Fatal("unverified global declaration became available")
	}

	// Check GetGlobalPluginsWithCapability
	plugins := GetGlobalPluginsWithCapability("runtime.create")
	if len(plugins) != 0 {
		t.Fatalf("unverified plugins were dispatchable: %v", plugins)
	}

	// Check RequireGlobalCapability
	err := RequireGlobalCapability("runtime.create")
	if err == nil {
		t.Fatal("unverified global capability satisfied requirement")
	}

	err = RequireGlobalCapability("nonexistent.cap")
	if err == nil {
		t.Fatal("expected error for nonexistent capability")
	}
}

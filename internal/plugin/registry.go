package plugin

import (
	"errors"
	"reflect"
	"slices"
	"sort"
	"sync"
)

// Registry indexes plugins by capability and family for verified dispatch.
type Registry struct {
	mu       sync.RWMutex
	plugins  map[string]Manifest // plugin name -> manifest
	byCap    map[string][]string // capability -> list of plugin names
	byFamily map[string][]string // family -> list of plugin names
	states   map[string]capabilityState
}

type capabilityState struct {
	declared, discovered, negotiated, verified bool
	client                                     *Client
	verifiedManifest                           Manifest
}

func cloneManifest(manifest Manifest) Manifest {
	clone := manifest
	clone.Capabilities = append([]string(nil), manifest.Capabilities...)
	clone.Platforms = append([]string(nil), manifest.Platforms...)
	clone.Permissions.Network = append([]string(nil), manifest.Permissions.Network...)
	clone.Permissions.Filesystem = append([]string(nil), manifest.Permissions.Filesystem...)
	clone.Permissions.Secrets = append([]string(nil), manifest.Permissions.Secrets...)
	if manifest.Signature != nil {
		signature := *manifest.Signature
		clone.Signature = &signature
	}
	return clone
}

// NewRegistry creates a new, empty plugin registry.
func NewRegistry() *Registry {
	return &Registry{
		plugins:  make(map[string]Manifest),
		byCap:    make(map[string][]string),
		byFamily: make(map[string][]string),
		states:   make(map[string]capabilityState),
	}
}

// Register indexes a declared plugin manifest.
func (r *Registry) Register(manifest Manifest) {
	r.register(manifest, false)
}

func (r *Registry) registerDiscovered(manifest Manifest) {
	r.register(manifest, true)
}

func (r *Registry) register(manifest Manifest, discovered bool) {
	manifest = cloneManifest(manifest)
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.plugins[manifest.Name]; ok {
		if reflect.DeepEqual(existing, manifest) {
			state := r.states[manifest.Name]
			state.discovered = state.discovered || discovered
			r.states[manifest.Name] = state
		}
		return
	}
	r.plugins[manifest.Name] = manifest
	state := r.states[manifest.Name]
	state.declared = true
	state.discovered = state.discovered || discovered
	r.states[manifest.Name] = state

	family := string(manifest.Family)
	r.byFamily[family] = append(r.byFamily[family], manifest.Name)

	for _, cap := range manifest.Capabilities {
		r.byCap[cap] = append(r.byCap[cap], manifest.Name)
	}
}

// Unregister removes a plugin from the registry.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()

	manifest, ok := r.plugins[name]
	if !ok {
		r.mu.Unlock()
		return
	}
	client := r.states[name].client

	delete(r.plugins, name)
	delete(r.states, name)
	removeIndexEntry(r.byFamily, string(manifest.Family), name)
	for _, cap := range manifest.Capabilities {
		removeIndexEntry(r.byCap, cap, name)
	}
	r.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

func removeIndexEntry(index map[string][]string, key, name string) {
	values := slices.DeleteFunc(index[key], func(value string) bool { return value == name })
	if len(values) == 0 {
		delete(index, key)
	} else {
		index[key] = values
	}
}

// GetManifest returns the manifest for a plugin by name.
func (r *Registry) GetManifest(name string) (Manifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	manifest, ok := r.plugins[name]
	return cloneManifest(manifest), ok
}

// HasCapability reports whether an available plugin provides cap.
func (r *Registry) HasCapability(cap string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, name := range r.byCap[cap] {
		if r.availableLocked(name) {
			return true
		}
	}
	return false
}

// DeclaredHasCapability reports manifest intent, including plugins that have
// not passed negotiation, trust verification, or the runtime liveness check.
// It is diagnostic discovery evidence and must not be used for dispatch.
func (r *Registry) DeclaredHasCapability(cap string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byCap[cap]) > 0
}

// GetPluginsWithCapability returns available providers in stable order.
func (r *Registry) GetPluginsWithCapability(cap string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []string
	for _, name := range r.byCap[cap] {
		if r.availableLocked(name) {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

// DeclaredPluginsWithCapability returns deterministic manifest declarations,
// regardless of whether those plugins are safe and alive enough to dispatch.
func (r *Registry) DeclaredPluginsWithCapability(cap string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := append([]string(nil), r.byCap[cap]...)
	sort.Strings(result)
	return result
}

func (r *Registry) availableLocked(name string) bool {
	state, ok := r.states[name]
	return ok && state.declared && state.discovered && state.negotiated && state.verified && state.client.isAlive()
}

// GetPluginsByFamily returns all plugin names in a given family.
func (r *Registry) GetPluginsByFamily(family string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := append([]string(nil), r.byFamily[family]...)
	sort.Strings(result)
	return result
}

// GetAllCapabilities returns all unique capabilities supported by registered plugins.
func (r *Registry) GetAllCapabilities() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	caps := make([]string, 0, len(r.byCap))
	for cap := range r.byCap {
		caps = append(caps, cap)
	}
	sort.Strings(caps)
	return caps
}

// GetAllPlugins returns all registered plugin names.
func (r *Registry) GetAllPlugins() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	plugins := make([]string, 0, len(r.plugins))
	for name := range r.plugins {
		plugins = append(plugins, name)
	}
	sort.Strings(plugins)
	return plugins
}

// publishAvailable atomically records evidence produced by the sole trust and
// handshake path. It is intentionally unexported so callers cannot manufacture
// availability from manifest declarations.
func (r *Registry) publishAvailable(manifest Manifest, client *Client) error {
	manifest = cloneManifest(manifest)
	r.mu.Lock()
	defer r.mu.Unlock()
	registered, ok := r.plugins[manifest.Name]
	if !ok {
		return errors.New("plugin is not registered")
	}
	if !reflect.DeepEqual(registered, manifest) {
		return errors.New("verified plugin does not match registered manifest")
	}
	state := r.states[manifest.Name]
	if !state.declared || !state.discovered {
		return errors.New("plugin was not discovered")
	}
	if !client.isAlive() {
		return errors.New("verified plugin client is not alive")
	}
	state.negotiated, state.verified = true, true
	state.client = client
	state.verifiedManifest = cloneManifest(manifest)
	r.states[manifest.Name] = state
	return nil
}

// PluginWithCapability holds a plugin reference with its capability information.
type PluginWithCapability struct {
	Name         string
	Manifest     Manifest
	Capabilities []string
}

// GetPluginWithCapability returns the first available matching plugin.
func (r *Registry) GetPluginWithCapability(cap string) (PluginWithCapability, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	pluginNames := append([]string(nil), r.byCap[cap]...)
	sort.Strings(pluginNames)
	for _, pluginName := range pluginNames {
		if !r.availableLocked(pluginName) {
			continue
		}
		manifest, ok := r.plugins[pluginName]
		if !ok {
			continue
		}
		manifest = cloneManifest(manifest)
		return PluginWithCapability{
			Name:         pluginName,
			Manifest:     manifest,
			Capabilities: append([]string(nil), manifest.Capabilities...),
		}, true
	}
	return PluginWithCapability{}, false
}

// RequireCapability fails when no available plugin provides cap.
func (r *Registry) RequireCapability(cap string) error {
	if !r.HasCapability(cap) {
		return &CapabilityError{
			Capability: cap,
			Message:    "no plugin supports this capability",
		}
	}
	return nil
}

// CapabilityError represents an error when a required capability is not available.
type CapabilityError struct {
	Capability string
	Message    string
}

func (e *CapabilityError) Error() string {
	return e.Message + ": " + e.Capability
}

// IsCapabilityError returns true if the error is a CapabilityError.
func IsCapabilityError(err error) bool {
	_, ok := err.(*CapabilityError)
	return ok
}

var globalRegistry = NewRegistry()

// GlobalRegistry returns the global plugin registry.
func GlobalRegistry() *Registry {
	return globalRegistry
}

// RegisterGlobal registers a plugin manifest in the global registry.
func RegisterGlobal(manifest Manifest) {
	globalRegistry.Register(manifest)
}

// UnregisterGlobal removes a plugin from the global registry.
func UnregisterGlobal(name string) {
	globalRegistry.Unregister(name)
}

// HasGlobalCapability returns true if the global registry has a plugin with the given capability.
func HasGlobalCapability(cap string) bool {
	return globalRegistry.HasCapability(cap)
}

// GetGlobalPluginsWithCapability returns all plugins with a given capability from the global registry.
func GetGlobalPluginsWithCapability(cap string) []string {
	return globalRegistry.GetPluginsWithCapability(cap)
}

// RequireGlobalCapability checks if the global registry has a plugin with the given capability.
func RequireGlobalCapability(cap string) error {
	return globalRegistry.RequireCapability(cap)
}

//go:build linux

package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const pluginSandboxHelperEnv = "PLATFORM_FACTORY_PLUGIN_SANDBOX_HELPER"

// pluginSandboxConfig holds configuration for plugin sandbox isolation.
type pluginSandboxConfig struct {
	// ProjectRoot, if non-empty, is the only directory the plugin can access.
	// If set and sandbox is available, the plugin runs with a mount namespace
	// that makes ProjectRoot appear as "/" (read-only).
	ProjectRoot string `json:"project_root,omitempty"`
	// Executable is the plugin executable path.
	Executable string `json:"executable"`
	// Args are the plugin arguments.
	Args []string `json:"args"`
}

// sandboxPayloadKey is used for the sandbox configuration payload.
// We keep the old pluginSandboxPayload for backward compatibility.
type pluginSandboxPayload struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
}

// wrapWithPluginSandbox rewrites cmd to re-exec the current binary inside a
// fresh user, network, IPC and UTS namespace with no host network access.
// MaybeApplyPluginSandboxHelper, which the consuming binary must call at the
// very start of main(), finishes the sandbox from inside: it sets
// no_new_privs and execs the real plugin target; it never returns on the
// helper path.
//
// This does not bound the plugin's memory or CPU. RLIMIT_AS - the only
// resource ceiling available without relying on cgroup delegation, which
// even a real Linux host commonly does not grant (confirmed empirically:
// see internal/executor's own cgroup-unavailable test skips) - applies to
// the process's *virtual* address space, and a fresh Go runtime needs on
// the order of 1 GiB of that just to boot, regardless of actual heap use;
// confirmed empirically that 256 MiB and 512 MiB both crash a trivial Go
// binary with "failed to reserve page summary memory" before it can even
// run. Since the plugin SDK is Go, and third-party plugins may be written
// in any language with its own bootstrap footprint, there is no single
// ceiling this pass could pick that is both safe for legitimate plugins and
// meaningfully protective; a resource limit here needs per-language or
// declared-by-manifest tuning, left as a follow-up.
//
// This still does not reduce the filesystem to a project-only view or hide
// host process IDs:
// detect, freeze and plan all take a real project path over the RPC
// protocol (api.FreezeParams.Root etc.) and read it directly, so confining
// that would need a mount namespace built around whichever project root
// the caller will later name - a larger change than this pass makes. There
// is nothing to isolate secrets from: no plugin capability ever receives
// secret material over the wire.
// PluginSandboxOptions configures sandbox isolation for plugins.
type PluginSandboxOptions struct {
	// ProjectRoot, if non-empty, will be the only directory accessible to the plugin.
	// This requirement fails closed until the host can demonstrate a mount/chroot
	// view containing only that directory. Callers may only waive it through the
	// explicitly named StartAllowingUnsandboxed policy entry point.
	ProjectRoot string
}

// DefaultPluginSandboxOptions returns the default sandbox options (no filesystem isolation).
func DefaultPluginSandboxOptions() PluginSandboxOptions {
	return PluginSandboxOptions{}
}

// WithProjectRoot returns sandbox options with the given project root for filesystem isolation.
func WithProjectRoot(root string) PluginSandboxOptions {
	return PluginSandboxOptions{ProjectRoot: root}
}

// Global sandbox options - can be set for all plugin starts
var globalSandboxOptions = DefaultPluginSandboxOptions()

// SetGlobalSandboxOptions sets the global sandbox options for all plugins.
func SetGlobalSandboxOptions(opts PluginSandboxOptions) {
	globalSandboxOptions = opts
}

// GetGlobalSandboxOptions returns the current global sandbox options.
func GetGlobalSandboxOptions() PluginSandboxOptions {
	return globalSandboxOptions
}

// PluginSandboxConfig returns the effective sandbox config, merging global options
// with any per-plugin options. This is exported for use by the Start function.
func PluginSandboxConfig(opts PluginSandboxOptions) PluginSandboxOptions {
	if opts.ProjectRoot == "" {
		return globalSandboxOptions
	}
	return opts
}

// pluginSandboxConfig is used internally to pass sandbox configuration.
// We use a var so tests can override it.
var pluginSandboxConfigFunc = func(cmd *exec.Cmd) PluginSandboxOptions {
	return PluginSandboxConfig(PluginSandboxOptions{})
}

// SetPluginSandboxConfigFunc allows tests to override sandbox configuration.
func SetPluginSandboxConfigFunc(f func(cmd *exec.Cmd) PluginSandboxOptions) func() {
	old := pluginSandboxConfigFunc
	pluginSandboxConfigFunc = f
	return func() {
		pluginSandboxConfigFunc = old
	}
}

func wrapWithPluginSandbox(cmd *exec.Cmd) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}

	// Get sandbox configuration
	sandboxOpts := pluginSandboxConfigFunc(cmd)
	if sandboxOpts.ProjectRoot != "" {
		// The former implementation merely hid a best-effort list of host paths
		// and changed cwd. That did not make ProjectRoot the only accessible
		// directory, and failures were silently ignored. Refuse the requirement
		// until a real mount-root/chroot implementation can be demonstrated.
		return fmt.Errorf("plugin: required project-root filesystem isolation is unavailable for %q", sandboxOpts.ProjectRoot)
	}

	// Backward compatible payload.
	oldPayload := pluginSandboxPayload{Executable: cmd.Path, Args: cmd.Args[1:]}
	payload, err := json.Marshal(oldPayload)
	if err != nil {
		return err
	}

	cmd.Path = self
	cmd.Args = []string{self}
	cmd.Env = append(cmd.Env, pluginSandboxHelperEnv+"="+string(payload))

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
	}
	return nil
}

// MaybeApplyPluginSandboxHelper must be called at the very start of main()
// by any binary that starts plugin subprocesses via Start. Inside the
// freshly created namespaces it sets no_new_privs, configures filesystem
// isolation if a project root is specified, then execs the real plugin
// binary; it never returns on the helper path.
func MaybeApplyPluginSandboxHelper() {
	raw := os.Getenv(pluginSandboxHelperEnv)
	if raw == "" {
		return
	}
	os.Unsetenv(pluginSandboxHelperEnv)

	// Try to parse as new config format first
	var config pluginSandboxConfig
	if err := json.Unmarshal([]byte(raw), &config); err == nil && config.Executable != "" {
		// New format with project root
		applySandboxWithConfig(config)
		return
	}

	// Fall back to old format for backward compatibility
	var payload pluginSandboxPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		pluginSandboxFatal("invalid helper payload", err)
	}
	applySandbox(payload.Executable, payload.Args)
}

// applySandbox applies the standard sandbox (without filesystem isolation).
func applySandbox(executable string, args []string) {
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, pluginPrSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		pluginSandboxFatal("set no_new_privs", errno)
	}
	applyResourceLimits()
	execPlugin(executable, args)
}

// applySandboxWithConfig applies sandbox with optional filesystem isolation.
func applySandboxWithConfig(config pluginSandboxConfig) {
	if config.ProjectRoot != "" {
		pluginSandboxFatal("project-root filesystem isolation unavailable", fmt.Errorf("cannot prove an isolated root for %q", config.ProjectRoot))
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, pluginPrSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		pluginSandboxFatal("set no_new_privs", errno)
	}
	applyResourceLimits()

	execPlugin(config.Executable, config.Args)
}

// applyResourceLimits sets resource limits for the plugin process.
func applyResourceLimits() {
	for resource, value := range map[int]uint64{
		syscall.RLIMIT_CORE:   0,
		syscall.RLIMIT_FSIZE:  16 << 20, // 16 MiB
		syscall.RLIMIT_NOFILE: 256,      // 256 open files
		syscall.RLIMIT_CPU:    60,       // 60 seconds CPU time
	} {
		limit := syscall.Rlimit{Cur: value, Max: value}
		if err := syscall.Setrlimit(resource, &limit); err != nil {
			// Resource limit setting may fail on some systems
			// Log but don't fatal - the sandbox still provides isolation
			fmt.Fprintf(os.Stderr, "plugin sandbox: set resource limit: %v\n", err)
		}
	}
}

// execPlugin executes the plugin binary with the configured argv and environment.
func execPlugin(executable string, args []string) {
	resolved, err := exec.LookPath(executable)
	if err != nil {
		pluginSandboxFatal("resolve executable", err)
	}
	argv := append([]string{executable}, args...)
	// Filter environment to remove sensitive variables
	// Keep only PATH and PLATFORM_FACTORY_* variables
	filteredEnv := filterPluginEnvironment(os.Environ())
	if err := syscall.Exec(resolved, argv, filteredEnv); err != nil {
		pluginSandboxFatal("exec", err)
	}
}

// filterPluginEnvironment filters the environment to remove sensitive variables.
// Only PATH and PLATFORM_FACTORY_* variables are kept.
func filterPluginEnvironment(env []string) []string {
	var filtered []string
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			filtered = append(filtered, e)
			continue
		}
		if strings.HasPrefix(e, "PLATFORM_FACTORY_") {
			filtered = append(filtered, e)
			continue
		}
		// Keep minimal set of variables that plugins might need
		if strings.HasPrefix(e, "LANG=") || strings.HasPrefix(e, "LC_") {
			filtered = append(filtered, e)
			continue
		}
	}
	// Ensure PATH is set
	if !hasEnvVar(filtered, "PATH") {
		filtered = append(filtered, "PATH=/usr/bin:/bin")
	}
	return filtered
}

// hasEnvVar checks if an environment variable is present in the list.
func hasEnvVar(env []string, name string) bool {
	prefix := name + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

func pluginSandboxFatal(what string, err error) {
	fmt.Fprintf(os.Stderr, "plugin sandbox: %s: %v\n", what, err)
	os.Exit(125)
}

const pluginPrSetNoNewPrivs = 38

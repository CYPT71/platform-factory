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
	// Family is the plugin's own declared Manifest.Family, carried across
	// the re-exec boundary so the sandboxed child can resolve its
	// PermissionProfile (permission_profile.go) from it. Empty for a start
	// with no manifest context (StartWithFamily's zero value, or the plain
	// Start entry point), which resolves to defaultPermissionProfile -
	// identical to this package's behavior before per-family profiles
	// existed.
	Family PluginFamily `json:"family,omitempty"`
	// Permissions is the plugin's own declared Manifest.Permissions,
	// carried across the re-exec boundary the same way Family is - see
	// wrapWithPluginSandbox's doc comment on hostNetworkGranted and
	// applySandboxWithConfig's filterPluginEnvironment call for what it
	// changes. Zero value grants nothing beyond the unconditional isolation
	// every plugin gets, identical to this package's behavior before this
	// field existed.
	Permissions PluginPermissions `json:"permissions,omitempty"`
}

// sandboxPayloadKey is used for the sandbox configuration payload.
// We keep the old pluginSandboxPayload for backward compatibility.
type pluginSandboxPayload struct {
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
}

// wrapWithPluginSandbox rewrites cmd to re-exec the current binary inside a
// fresh user, IPC and UTS namespace, plus a fresh network namespace with no
// host network access - unless hostNetworkGranted(family, permissions)
// says otherwise (see its own doc comment). MaybeApplyPluginSandboxHelper,
// which the consuming binary must call at the very start of main(), finishes
// the sandbox from inside: it sets no_new_privs and execs the real plugin
// target; it never returns on the helper path.
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

// hostNetworkGranted reports whether a plugin should keep the host's real
// network namespace instead of the isolated, connectivity-less one every
// plugin gets by default. This is deliberately coarse - all of the host's
// network or none of it, not a per-destination allowlist - because building
// real per-endpoint enforcement (a veth pair plus an nftables allowlist keyed
// off Permissions.Network's entries) is a larger change than this pass
// makes; see manifest.go's Validate doc comment, which already says the
// containerd/KubeVirt permission tiers "don't reduce to a boolean check
// against this model" and are not enforced there either. What this does
// enforce: a plugin only gets real connectivity when its own signed manifest
// declares Permissions.Network is non-empty, and never for the language
// family regardless of what it declares (Validate already refuses a
// language-family manifest that declares any network permission at all, so
// the family check here is defense in depth, not the only guard). A plugin
// that needs to reach a real service - KubeVirt/containerd talking to the
// Kubernetes API, for example - cannot do that work at all inside the
// default fresh network namespace, which has only loopback.
func hostNetworkGranted(family PluginFamily, permissions PluginPermissions) bool {
	return family != PluginFamilyLanguage && len(permissions.Network) > 0
}

func wrapWithPluginSandbox(cmd *exec.Cmd, family PluginFamily, permissions PluginPermissions) error {
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

	config := pluginSandboxConfig{Executable: cmd.Path, Args: cmd.Args[1:], Family: family, Permissions: permissions}
	payload, err := json.Marshal(config)
	if err != nil {
		return err
	}

	cmd.Path = self
	cmd.Args = []string{self}
	cmd.Env = append(cmd.Env, pluginSandboxHelperEnv+"="+string(payload))

	cloneflags := syscall.CLONE_NEWUSER | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNS
	if !hostNetworkGranted(family, permissions) {
		cloneflags |= syscall.CLONE_NEWNET
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// CLONE_NEWNS: gives the plugin its own mount table so
		// isolateTempDirectory (applyResourceLimits' caller, below) can
		// mount a fresh, empty tmpfs over /tmp without touching the host's
		// or any other plugin's - unshare(CLONE_NEWNS) needs no privilege
		// beyond the CLONE_NEWUSER already required immediately before it
		// here, the standard unprivileged-user-namespace-plus-mount-
		// namespace combination rootless container runtimes rely on.
		Cloneflags: uintptr(cloneflags),
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

// applySandbox applies the standard sandbox (without filesystem isolation),
// using defaultPermissionProfile: this is the pluginSandboxPayload
// (old-format, family-less) fallback path in MaybeApplyPluginSandboxHelper.
func applySandbox(executable string, args []string) {
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, pluginPrSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		pluginSandboxFatal("set no_new_privs", errno)
	}
	pinned := openExecutable(executable)
	applyResourceLimits(defaultPermissionProfile)
	isolateTempDirectory()
	execPlugin(executable, pinned, args, PluginPermissions{})
}

// applySandboxWithConfig applies sandbox with optional filesystem isolation,
// using the PermissionProfile config.Family resolves to.
func applySandboxWithConfig(config pluginSandboxConfig) {
	if config.ProjectRoot != "" {
		pluginSandboxFatal("project-root filesystem isolation unavailable", fmt.Errorf("cannot prove an isolated root for %q", config.ProjectRoot))
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, pluginPrSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		pluginSandboxFatal("set no_new_privs", errno)
	}
	pinned := openExecutable(config.Executable)
	applyResourceLimits(permissionProfileFor(config.Family))
	isolateTempDirectory()

	execPlugin(config.Executable, pinned, config.Args, config.Permissions)
}

// applyResourceLimits sets resource limits for the plugin process from
// profile. Scoped safely by the fresh CLONE_NEWUSER namespace
// wrapWithPluginSandbox already requires: RLIMIT_NPROC's and RLIMIT_AS's
// kernel accounting keys off (user namespace, uid) and the calling process
// itself respectively, so neither can cap resources for the host user's
// other, unrelated processes the way setting RLIMIT_NPROC in the host's own
// namespace would.
func applyResourceLimits(profile PermissionProfile) {
	limits := map[int]uint64{
		syscall.RLIMIT_CORE:   0,
		syscall.RLIMIT_FSIZE:  16 << 20, // 16 MiB
		syscall.RLIMIT_NOFILE: 256,      // 256 open files
	}
	if profile.CPUSeconds > 0 {
		limits[syscall.RLIMIT_CPU] = profile.CPUSeconds
	}
	if profile.Processes > 0 {
		limits[rlimitNPROC] = profile.Processes
	}
	if profile.MemoryMiB > 0 {
		limits[syscall.RLIMIT_AS] = profile.MemoryMiB << 20
	}
	for resource, value := range limits {
		limit := syscall.Rlimit{Cur: value, Max: value}
		if err := syscall.Setrlimit(resource, &limit); err != nil {
			// Resource limit setting may fail on some systems
			// Log but don't fatal - the sandbox still provides isolation
			fmt.Fprintf(os.Stderr, "plugin sandbox: set resource limit: %v\n", err)
		}
	}
}

// isolateTempDirectory gives the plugin its own private scratch tmpfs,
// exposed to it as $TMPDIR, inside the mount namespace wrapWithPluginSandbox's
// CLONE_NEWNS just created - so a plugin's own incidental temp-file use
// (library scratch files, atomic-write staging, ...) can never read or leave
// files where another process or another plugin's own scratch use could see
// them, and each plugin start gets a fresh one.
//
// This deliberately does NOT mount over /tmp itself, unlike an earlier
// version of this function: detect/freeze/plan (see this file's own doc
// comment above) take a real, caller-chosen project path over the RPC
// protocol, and that path routinely lives under /tmp - both in this
// package's own tests (t.TempDir()) and in real staged/extracted-archive
// project roots. Masking all of /tmp broke every one of those paths from
// the plugin's point of view. A mount namespace confined to exactly the
// caller-named project root (and nothing else) is the real fix for "a
// plugin can only see what it was handed" - see the ProjectRoot isolation
// above, which still fails closed until that exists - and is out of scope
// for this pass, same as that comment already says.
//
// First makes the root mount private (MS_REC|MS_PRIVATE) so this mount
// cannot propagate back out to the host or a sibling namespace, the same
// first step internal/hypervisor/sandbox's VMM mount isolation uses for the
// same reason. Best-effort like applyResourceLimits: a host where this
// fails still gets every other isolation property this sandbox provides,
// just with $TMPDIR left at its inherited value.
func isolateTempDirectory() {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		fmt.Fprintf(os.Stderr, "plugin sandbox: make mount namespace private: %v\n", err)
		return
	}
	scratch := fmt.Sprintf("/tmp/.platform-factory-plugin-scratch-%d", os.Getpid())
	if err := os.Mkdir(scratch, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "plugin sandbox: create private scratch directory: %v\n", err)
		return
	}
	if err := syscall.Mount("tmpfs", scratch, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "size=64m,mode=0700"); err != nil {
		fmt.Fprintf(os.Stderr, "plugin sandbox: isolate temp directory: %v\n", err)
		return
	}
	os.Setenv("TMPDIR", scratch)
}

// openExecutable resolves executable via PATH and opens it, pinning that
// exact inode to a file descriptor before isolateTempDirectory (called
// between this and execPlugin) has any chance to mount over the directory
// it lives in - the common case for a freshly staged or extracted plugin
// binary, which this package's own tests build directly under /tmp. Once
// open, the descriptor identifies the file directly at the kernel level, so
// it stays execable via /proc/self/fd regardless of whether its original
// path is later masked or removed - the standard open-before-exec pattern
// fexecve(3) uses internally on Linux for exactly this TOCTOU-safety
// property, not just for surviving a later mount.
func openExecutable(executable string) *os.File {
	resolved, err := exec.LookPath(executable)
	if err != nil {
		pluginSandboxFatal("resolve executable", err)
	}
	file, err := os.Open(resolved)
	if err != nil {
		pluginSandboxFatal("open executable", err)
	}
	return file
}

// execPlugin executes the plugin binary pinned by pinned (see
// openExecutable) with the configured argv and environment. argv[0] stays
// the original, pre-isolation name for any plugin that inspects it; the
// path actually exec'd is /proc/self/fd/<pinned>, immune to isolateTempDirectory
// having masked pinned's original path in between.
func execPlugin(executable string, pinned *os.File, args []string, permissions PluginPermissions) {
	defer pinned.Close()
	fdPath := fmt.Sprintf("/proc/self/fd/%d", pinned.Fd())
	argv := append([]string{executable}, args...)
	// Filter environment to remove sensitive variables
	// Keep only PATH and PLATFORM_FACTORY_* variables
	filteredEnv := filterPluginEnvironment(os.Environ(), permissions)
	if err := syscall.Exec(fdPath, argv, filteredEnv); err != nil {
		pluginSandboxFatal("exec", err)
	}
}

// declaresKubeconfigSecret reports whether permissions names "kubeconfig"
// among its declared secrets - the same string kubevirt's own plugin.json
// declares (see plugins/kubevirt/plugin.json's permissions.secrets).
func declaresKubeconfigSecret(permissions PluginPermissions) bool {
	for _, secret := range permissions.Secrets {
		if secret == "kubeconfig" {
			return true
		}
	}
	return false
}

// filterPluginEnvironment filters the environment to remove sensitive
// variables. Only PATH, PLATFORM_FACTORY_*, LANG/LC_* and TMPDIR are kept
// unconditionally; KUBECONFIG and HOME are kept only when permissions
// declares "kubeconfig" among its secrets, since only a plugin whose own
// signed manifest asks for cluster credentials (KubeVirt, today) has any
// legitimate use for them - every other plugin, including one that somehow
// also got host network access, still cannot locate or read them.
func filterPluginEnvironment(env []string, permissions PluginPermissions) []string {
	needsKubeconfig := declaresKubeconfigSecret(permissions)
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
		if needsKubeconfig && (strings.HasPrefix(e, "KUBECONFIG=") || strings.HasPrefix(e, "HOME=")) {
			filtered = append(filtered, e)
			continue
		}
		// isolateTempDirectory sets this to the plugin's own private
		// scratch tmpfs; without it a filtered-out TMPDIR would silently
		// fall back to the unfiltered value the exec'd plugin inherits
		// from its own environment lookup default (/tmp), defeating the
		// isolation applyResourceLimits' caller just set up.
		if strings.HasPrefix(e, "TMPDIR=") {
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

// rlimitNPROC is RLIMIT_NPROC (include/uapi/asm-generic/resource.h): unlike
// RLIMIT_CORE/FSIZE/NOFILE/CPU/AS above, Go's syscall package has never
// exported a constant for it on any platform - the same class of stdlib gap
// internal/hypervisor/sandbox/syscalls_linux.go documents and works around
// for getrandom/copy_file_range/rseq/clone3, via the same fix: the raw,
// long-stable numeric value.
const rlimitNPROC = 6

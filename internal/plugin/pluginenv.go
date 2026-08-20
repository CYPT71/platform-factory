package plugin

import "strings"

// declaresKubeconfigSecret reports whether permissions names "kubeconfig"
// among its declared secrets - the same string kubevirt's own plugin.json
// declares (see plugins/kubevirt/plugin.json's permissions.secrets).
//
// This function and filterPluginEnvironment below live in this
// cross-platform (no build tag) file rather than sandbox_linux.go, even
// though they were originally written there: client.go's newPluginCommand,
// which is what every plugin start actually goes through on every platform,
// needs to call filterPluginEnvironment to compute a plugin's real
// environment - and on non-Linux, wrapWithPluginSandbox (sandbox_other.go)
// always fails, so every plugin start there takes the unsandboxed fallback
// through newPluginCommand. Neither function uses anything Linux-specific;
// both are pure string/slice logic over an []string env and a
// PluginPermissions value.
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
// signed manifest asks for cluster credentials (KubeVirt, the kubernetes
// deploy plugin) has any legitimate use for them - every other plugin,
// including one that somehow also got host network access, still cannot
// locate or read them.
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
		// isolateTempDirectory (sandbox_linux.go) sets this to the plugin's
		// own private scratch tmpfs; without it a filtered-out TMPDIR would
		// silently fall back to the unfiltered value the exec'd plugin
		// inherits from its own environment lookup default (/tmp), defeating
		// the isolation applyResourceLimits' caller just set up.
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

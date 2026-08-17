//go:build !linux

package plugin

import (
	"errors"
	"os/exec"
)

// wrapWithPluginSandbox reports that no sandbox facility exists on this
// platform. Namespaces are a Linux kernel feature; Start falls back to an
// unsandboxed launch when this returns an error, exactly as it does when
// the Linux implementation's namespace creation itself fails.
func wrapWithPluginSandbox(*exec.Cmd, PluginFamily, PluginPermissions) error {
	return errors.New("plugin sandboxing is not available on this platform")
}

// MaybeApplyPluginSandboxHelper is a no-op on this platform. Safe to call
// unconditionally from main() on any platform.
func MaybeApplyPluginSandboxHelper() {}

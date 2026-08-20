package plugin

import (
	"strings"
	"testing"
)

func TestFilterPluginEnvironment(t *testing.T) {
	// Test that sensitive environment variables are filtered out
	env := []string{
		"PATH=/usr/bin:/bin",
		"PLATFORM_FACTORY_TRACE_ID=test123",
		"HOME=/home/user",
		"SECRET_PASSWORD=dontleak",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"SSH_AUTH_SOCK=/tmp/ssh",
		"AWS_SECRET_ACCESS_KEY=secret",
	}

	filtered := filterPluginEnvironment(env, PluginPermissions{})

	// Check that PATH is kept
	found := false
	for _, e := range filtered {
		if strings.HasPrefix(e, "PATH=") {
			found = true
			if e != "PATH=/usr/bin:/bin" {
				t.Errorf("expected filtered PATH to be /usr/bin:/bin, got %q", e)
			}
			break
		}
	}
	if !found {
		t.Error("expected PATH to be in filtered environment")
	}

	// Check that PLATFORM_FACTORY_* is kept
	found = false
	for _, e := range filtered {
		if strings.HasPrefix(e, "PLATFORM_FACTORY_") {
			found = true
			if e != "PLATFORM_FACTORY_TRACE_ID=test123" {
				t.Errorf("expected PLATFORM_FACTORY_TRACE_ID, got %q", e)
			}
			break
		}
	}
	if !found {
		t.Error("expected PLATFORM_FACTORY_TRACE_ID to be in filtered environment")
	}

	// Check that sensitive variables are removed
	sensitive := []string{"HOME=", "SECRET_", "SSH_AUTH_SOCK=", "AWS_SECRET"}
	for _, prefix := range sensitive {
		for _, e := range filtered {
			if strings.HasPrefix(e, prefix) {
				t.Errorf("sensitive variable %q should be filtered out, but found in: %v", prefix, filtered)
			}
		}
	}

	// Check that LANG/LC variables are kept
	found = false
	for _, e := range filtered {
		if strings.HasPrefix(e, "LANG=") || strings.HasPrefix(e, "LC_") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected LANG or LC_ variables to be in filtered environment")
	}
}

// TestFilterPluginEnvironmentGrantsKubeconfigOnlyWhenDeclared proves
// KUBECONFIG/HOME pass through only for a plugin whose own Permissions
// declares "kubeconfig" as a secret - not for every plugin, and not merely
// because it happens to also have host network access (a separate,
// independent grant - see hostNetworkGranted in sandbox_linux.go).
func TestFilterPluginEnvironmentGrantsKubeconfigOnlyWhenDeclared(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin", "HOME=/home/user", "KUBECONFIG=/home/user/.kube/config"}

	withoutGrant := filterPluginEnvironment(env, PluginPermissions{Network: []string{"kubernetes-api"}})
	for _, e := range withoutGrant {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "KUBECONFIG=") {
			t.Fatalf("HOME/KUBECONFIG leaked to a plugin that did not declare the kubeconfig secret: %v", withoutGrant)
		}
	}

	withGrant := filterPluginEnvironment(env, PluginPermissions{Secrets: []string{"kubeconfig"}})
	var sawHome, sawKubeconfig bool
	for _, e := range withGrant {
		sawHome = sawHome || e == "HOME=/home/user"
		sawKubeconfig = sawKubeconfig || e == "KUBECONFIG=/home/user/.kube/config"
	}
	if !sawHome || !sawKubeconfig {
		t.Fatalf("HOME/KUBECONFIG not passed through to a plugin that declared the kubeconfig secret: %v", withGrant)
	}
}

// TestFilterPluginEnvironmentIsIdempotent proves that filtering an
// already-filtered environment a second time through the same predicate is
// a no-op - the situation the sandboxed path now produces on Linux:
// newPluginCommand (client.go) filters the host's real os.Environ() once to
// seed cmd.Env before wrapWithPluginSandbox re-execs into the sandbox
// helper, and execPlugin (sandbox_linux.go) filters the helper's own
// os.Environ() - which is exactly that already-filtered set, plus the
// sandbox payload variable - a second time. If double-filtering were not
// idempotent, that second pass could silently drop something the first
// pass legitimately kept.
func TestFilterPluginEnvironmentIsIdempotent(t *testing.T) {
	env := []string{
		"PATH=/usr/bin:/bin",
		"PLATFORM_FACTORY_TRACE_ID=abc",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"TMPDIR=/tmp/plugin-scratch",
		"HOME=/home/user",
		"KUBECONFIG=/home/user/.kube/config",
	}
	for _, permissions := range []PluginPermissions{
		{},
		{Secrets: []string{"kubeconfig"}},
	} {
		once := filterPluginEnvironment(env, permissions)
		twice := filterPluginEnvironment(once, permissions)
		if strings.Join(once, "\n") != strings.Join(twice, "\n") {
			t.Fatalf("filtering twice (permissions=%+v) is not idempotent:\nonce:  %v\ntwice: %v", permissions, once, twice)
		}
	}
}

func TestDeclaresKubeconfigSecret(t *testing.T) {
	if declaresKubeconfigSecret(PluginPermissions{}) {
		t.Fatal("zero-value permissions declared the kubeconfig secret")
	}
	if declaresKubeconfigSecret(PluginPermissions{Secrets: []string{"other-secret"}}) {
		t.Fatal("an unrelated declared secret was treated as declaring kubeconfig")
	}
	if !declaresKubeconfigSecret(PluginPermissions{Secrets: []string{"other-secret", "kubeconfig"}}) {
		t.Fatal("kubeconfig secret not detected among multiple declared secrets")
	}
}

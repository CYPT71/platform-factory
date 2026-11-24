package plugin

import (
	"context"
	"strings"
	"testing"
)

func TestNetworkPolicyEnforcesPerDestinationAllowlistAndBlocksMetadata(t *testing.T) {
	policy, err := ParseNetworkPolicy([]string{
		"https://registry.example",
		"*.k8s.internal:6443",
		"10.0.0.0/24",
	})
	if err != nil {
		t.Fatalf("ParseNetworkPolicy: %v", err)
	}
	if policy.HasBroadPermission() {
		t.Fatal("expected narrow policy not to be flagged broad")
	}
	if !policy.Allows("registry.example", "443") {
		t.Fatal("expected exact host:443 to be allowed")
	}
	if policy.Allows("registry.example", "80") {
		t.Fatal("expected non-matching port 80 to be denied")
	}
	if !policy.Allows("api.k8s.internal", "6443") {
		t.Fatal("expected wildcard subdomain match to be allowed")
	}
	if policy.Allows("evil.example", "443") {
		t.Fatal("expected undeclared external host to be denied")
	}
	if !policy.Allows("10.0.0.42", "8080") {
		t.Fatal("expected CIDR match to be allowed")
	}
	if policy.Allows("169.254.169.254", "80") {
		t.Fatal("cloud metadata endpoint must be denied")
	}

	broad, err := ParseNetworkPolicy([]string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if !broad.HasBroadPermission() {
		t.Fatal("expected * to be flagged as broad permission")
	}
	if broad.Allows("169.254.169.254", "80") || broad.Allows("metadata.google.internal", "80") {
		t.Fatal("even broad policy must deny cloud metadata endpoints unless explicitly named")
	}

	dial := policy.SafeDialContext(nil)
	if _, err := dial(context.Background(), "tcp", "evil.example:443"); err == nil || !strings.Contains(err.Error(), "not in manifest allowlist") {
		t.Fatalf("expected SafeDialContext refusal, got %v", err)
	}
}

func TestFilterPluginEnvironmentStripsPlatformFactorySecrets(t *testing.T) {
	env := []string{
		"PATH=/usr/bin:/bin",
		"PLATFORM_FACTORY_TRACE_ID=v1-cli-123",
		"PLATFORM_FACTORY_REGISTRY_PASSWORD=top-secret",
		"PLATFORM_FACTORY_API_TOKEN=tok-secret",
		"PLATFORM_FACTORY_PRIVATE_KEY=priv-secret",
		"AWS_SECRET_ACCESS_KEY=aws-secret",
	}
	filtered := filterPluginEnvironment(env, PluginPermissions{})
	joined := strings.Join(filtered, "\n")
	if !strings.Contains(joined, "PLATFORM_FACTORY_TRACE_ID=v1-cli-123") {
		t.Fatalf("expected non-sensitive PLATFORM_FACTORY_TRACE_ID to survive: %s", joined)
	}
	for _, secret := range []string{"top-secret", "tok-secret", "priv-secret", "aws-secret"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("secret %q leaked into plugin environment:\n%s", secret, joined)
		}
	}
}

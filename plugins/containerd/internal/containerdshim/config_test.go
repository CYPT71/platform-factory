package containerdshim

import (
	"strings"
	"testing"
)

func TestContainerdConfigSelectsShimSandboxer(t *testing.T) {
	got, err := (Config{}).ContainerdConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`runtimes.platform-factory]`,
		`runtime_type = "io.containerd.platform-factory.v1"`,
		`sandboxer = "shim"`,
		`privileged_without_host_devices = false`,
		`pod_annotations = ["platform-factory.dev/*", "secure-oci.dev/*"]`,
		`container_annotations = ["platform-factory.dev/*", "secure-oci.dev/*"]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "proxy") {
		t.Fatalf("config unexpectedly selects a proxy: %s", got)
	}
}

func TestRuntimeClassMatchesHandler(t *testing.T) {
	got, err := (Config{Handler: "platform-factory"}).RuntimeClass()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "name: platform-factory: platform-factory") {
		t.Fatalf("unexpected RuntimeClass:\n%s", got)
	}
}

func TestConfigRejectsUnsafeHandlers(t *testing.T) {
	tests := []Config{
		{Handler: "UPPER"},
		{Handler: "-bad"},
		{Handler: strings.Repeat("a", 64)},
	}
	for _, test := range tests {
		if _, err := test.ContainerdConfig(); err == nil {
			t.Fatalf("ContainerdConfig(%+v) succeeded", test)
		}
	}
}

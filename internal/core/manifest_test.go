package core

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// exampleManifestYAML mirrors Sanetizer-todo.md item 11's own example
// verbatim, so this test proves the schema actually accepts the manifest
// shape the roadmap specifies, not just a shape convenient to the
// implementation.
const exampleManifestYAML = `
id: kubevirt
version: 1.2.0
protocol_version: 1
family: deployment
capabilities:
  - deployment.plan
  - deployment.apply
  - deployment.observe
permissions:
  network:
    - kubernetes-api
  filesystem:
    - none
  secrets:
    - kubeconfig
`

func TestPluginManifestDecodesTheRoadmapExampleYAML(t *testing.T) {
	var manifest PluginManifest
	if err := yaml.Unmarshal([]byte(exampleManifestYAML), &manifest); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("expected the roadmap's own example to validate, got: %v", err)
	}
	if manifest.ID != "kubevirt" || manifest.Version != "1.2.0" || manifest.ProtocolVersion != 1 {
		t.Fatalf("manifest=%+v", manifest)
	}
	if !manifest.HasCapability("deployment.apply") {
		t.Fatal("expected deployment.apply to be a declared capability")
	}
	if manifest.HasCapability("deployment.rollback") {
		t.Fatal("did not expect an undeclared capability to be reported as present")
	}
	if len(manifest.Permissions.Network) != 1 || manifest.Permissions.Network[0] != "kubernetes-api" {
		t.Fatalf("permissions=%+v", manifest.Permissions)
	}
}

func TestPluginManifestValidateRequiresID(t *testing.T) {
	m := PluginManifest{Version: "1.0.0", ProtocolVersion: 1, Family: PluginFamilyLanguage}
	if err := m.Validate(); err == nil {
		t.Fatal("expected an error for a missing id")
	}
}

func TestPluginManifestValidateRequiresVersion(t *testing.T) {
	m := PluginManifest{ID: "x", ProtocolVersion: 1, Family: PluginFamilyLanguage}
	if err := m.Validate(); err == nil {
		t.Fatal("expected an error for a missing version")
	}
}

func TestPluginManifestValidateRequiresPositiveProtocolVersion(t *testing.T) {
	for _, v := range []int{0, -1} {
		m := PluginManifest{ID: "x", Version: "1.0.0", ProtocolVersion: v, Family: PluginFamilyLanguage}
		if err := m.Validate(); err == nil {
			t.Fatalf("protocol_version=%d: expected an error", v)
		}
	}
}

func TestPluginManifestValidateRejectsUnknownFamily(t *testing.T) {
	m := PluginManifest{ID: "x", Version: "1.0.0", ProtocolVersion: 1, Family: "not-a-real-family"}
	if err := m.Validate(); err == nil {
		t.Fatal("expected an error for an unrecognized family")
	}
}

func TestPluginManifestValidateAcceptsEveryDocumentedFamily(t *testing.T) {
	for _, family := range []PluginFamily{
		PluginFamilyLanguage, PluginFamilyAnalyzer, PluginFamilyBuild,
		PluginFamilyRuntime, PluginFamilyDeployment, PluginFamilyCapability,
	} {
		m := PluginManifest{ID: "x", Version: "1.0.0", ProtocolVersion: 1, Family: family}
		if err := m.Validate(); err != nil {
			t.Errorf("family=%q: unexpected error: %v", family, err)
		}
	}
}

func TestPluginManifestValidateRejectsDuplicateCapabilities(t *testing.T) {
	m := PluginManifest{
		ID: "x", Version: "1.0.0", ProtocolVersion: 1, Family: PluginFamilyRuntime,
		Capabilities: []string{"runtime.create", "runtime.create"},
	}
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err=%v, want a duplicate-capability error", err)
	}
}

func TestPluginManifestValidateRejectsEmptyCapabilityEntry(t *testing.T) {
	m := PluginManifest{
		ID: "x", Version: "1.0.0", ProtocolVersion: 1, Family: PluginFamilyRuntime,
		Capabilities: []string{""},
	}
	if err := m.Validate(); err == nil {
		t.Fatal("expected an error for an empty capability entry")
	}
}

func TestPluginManifestZeroValuePermissionsIsMostRestrictive(t *testing.T) {
	var p PluginPermissions
	if len(p.Network) != 0 || len(p.Filesystem) != 0 || len(p.Secrets) != 0 {
		t.Fatal("zero-value PluginPermissions should grant nothing")
	}
}

func TestSupportsProtocolMatchesOnlyListedVersions(t *testing.T) {
	m := PluginManifest{ID: "x", Version: "1.0.0", ProtocolVersion: 2, Family: PluginFamilyBuild}
	if m.SupportsProtocol(1) {
		t.Fatal("protocol_version=2 should not match host support for [1]")
	}
	if !m.SupportsProtocol(1, 2, 3) {
		t.Fatal("protocol_version=2 should match host support for [1,2,3]")
	}
	if m.SupportsProtocol() {
		t.Fatal("no host-supported versions should never match")
	}
}

package migration

import (
	"strings"
	"testing"
)

func validCanonicalPlan(t *testing.T) MigrationPlan {
	t.Helper()
	a := TestResource("a", "vm")
	b := TestResource("b", "disk")
	p := MigrationPlan{
		Version:         FormatVersion,
		DiscoveryStatus: DiscoveryStatusPartial,
		Resources:       []Resource{b, a},
		Graph: []DependencyEdge{{
			From: resourceIDFor(a), To: resourceIDFor(b), Relation: "depends_on", Required: true,
		}},
		Steps:    []MigrationStep{{OperationID: "op-b", ResourceID: resourceIDFor(b), Capability: "apply", Action: "create", Status: "pending"}, {OperationID: "op-a", ResourceID: resourceIDFor(a), Capability: "apply", Action: "create", Status: "pending"}},
		Gaps:     []CompatibilityGap{{ResourceID: a.ID, Requirement: "runtime.vm.apply", Reason: "conversion", Status: CompatibilityAdaptable}},
		Unknowns: []UnknownObservation{{SourcePlugin: "source", ObservationType: "opaque", Scope: "network", Reason: "not representable"}},
	}
	if err := p.SetDigest(); err != nil {
		t.Fatal(err)
	}
	return p
}

func resourceIDFor(r Resource) ResourceID {
	return ResourceID{PluginID: r.Source.PluginID, NativeType: r.Source.NativeType, NativeID: r.Source.NativeID}
}

func TestMigrationPlanCanonicalDigestIsOrderIndependent(t *testing.T) {
	p := validCanonicalPlan(t)
	q := p
	q.Resources = []Resource{p.Resources[1], p.Resources[0]}
	q.Steps = []MigrationStep{p.Steps[1], p.Steps[0]}
	if err := q.SetDigest(); err != nil {
		t.Fatal(err)
	}
	if p.Digest != q.Digest {
		t.Fatalf("digest depends on input order: %q != %q", p.Digest, q.Digest)
	}
	if !strings.HasPrefix(p.Digest, "sha256:") {
		t.Fatalf("unexpected digest format %q", p.Digest)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("valid sealed plan: %v", err)
	}
	p.Resources[0].Kind = "changed"
	if err := p.Validate(); !IsValidationError(err) {
		t.Fatalf("tampered plan accepted: %v", err)
	}
}

func TestMigrationPlanCanonicalDigestOrdersAllGapAndUnknownFields(t *testing.T) {
	p := validCanonicalPlan(t)
	p.Gaps = append(p.Gaps,
		CompatibilityGap{ResourceID: p.Resources[0].ID, Requirement: "storage.block.import", Status: CompatibilityDegraded, Reason: "conversion"})
	p.Unknowns = append(p.Unknowns,
		UnknownObservation{SourcePlugin: "source", ObservationType: "opaque-a", Scope: "network", Reason: "not representable"},
		UnknownObservation{SourcePlugin: "source", ObservationType: "opaque-b", Scope: "network", Reason: "not representable"})
	if err := p.SetDigest(); err != nil {
		t.Fatal(err)
	}
	q := p
	q.Gaps = []CompatibilityGap{p.Gaps[1], p.Gaps[0]}
	q.Unknowns = []UnknownObservation{p.Unknowns[2], p.Unknowns[1], p.Unknowns[0]}
	if err := q.SetDigest(); err != nil {
		t.Fatal(err)
	}
	if p.Digest != q.Digest {
		t.Fatalf("digest depends on equal-prefix ordering: %s != %s", p.Digest, q.Digest)
	}
}

func TestMigrationPlanSemanticValidation(t *testing.T) {
	tests := map[string]func(*MigrationPlan){
		"duplicate resource":    func(p *MigrationPlan) { p.Resources = append(p.Resources, p.Resources[0]) },
		"unknown graph ref":     func(p *MigrationPlan) { p.Graph[0].To.NativeID = "missing" },
		"duplicate operation":   func(p *MigrationPlan) { p.Steps[1].OperationID = p.Steps[0].OperationID },
		"invalid discovery":     func(p *MigrationPlan) { p.DiscoveryStatus = "unknown" },
		"invalid compatibility": func(p *MigrationPlan) { p.Gaps[0].Status = "maybe" },
		"secret attribute":      func(p *MigrationPlan) { p.Resources[0].Attributes["api_key"] = "sentinel" },
		"nested secret": func(p *MigrationPlan) {
			p.Resources[0].Attributes["config"] = map[string]interface{}{"password": "sentinel"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := validCanonicalPlan(t)
			mutate(&p)
			if _, err := p.ComputeDigest(); !IsValidationError(err) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
}

func TestMigrationPlanYAMLStrictAndPreservesDiscoveryStatus(t *testing.T) {
	p := validCanonicalPlan(t)
	b, err := MarshalYAML(&p)
	if err != nil {
		t.Fatal(err)
	}
	var got MigrationPlan
	if err := UnmarshalYAML(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.DiscoveryStatus != DiscoveryStatusPartial {
		t.Fatalf("status lost: %q", got.DiscoveryStatus)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("round-trip invalid: %v\nwant=%#v\ngot=%#v\nyaml=%s", err, p, got, b)
	}
	if err := UnmarshalYAML([]byte("version: v1\nunknown_field: true\n"), &got); err == nil {
		t.Fatal("unknown YAML field accepted")
	}
}

func FuzzMigrationPlanYAML(f *testing.F) {
	f.Add([]byte("version: v1\ndiscovery_status: complete\ndigest: sha256:00\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var got MigrationPlan
		if UnmarshalYAML(data, &got) != nil {
			return
		}
		if got.Digest != "" {
			_ = got.Validate()
		}
	})
}

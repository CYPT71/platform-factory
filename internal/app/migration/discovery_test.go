package migration

import (
	"context"
	"errors"
	"reflect"
	"testing"

	domainmigration "github.com/CYPT71/secure-oci-base/internal/migration"
)

type pageSource struct {
	id      string
	pages   map[string]DiscoveryPage
	errs    map[string]error
	cursors []string
	call    func(context.Context) error
}

func (s *pageSource) SourceID() string { return s.id }
func (s *pageSource) DiscoverPage(ctx context.Context, cursor string) (DiscoveryPage, error) {
	s.cursors = append(s.cursors, cursor)
	if s.call != nil {
		if err := s.call(ctx); err != nil {
			return DiscoveryPage{}, err
		}
	}
	if err := s.errs[cursor]; err != nil {
		return DiscoveryPage{}, err
	}
	return s.pages[cursor], nil
}

func TestDiscoverEmptyAndNormal(t *testing.T) {
	tests := []struct {
		name   string
		pages  map[string]DiscoveryPage
		count  int
		status domainmigration.DiscoveryStatus
	}{
		{name: "empty", pages: map[string]DiscoveryPage{"": {Status: domainmigration.DiscoveryComplete}}, status: domainmigration.DiscoveryComplete},
		{name: "normal", pages: map[string]DiscoveryPage{"": {Status: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{resource("b"), resource("a")}}}, count: 2, status: domainmigration.DiscoveryComplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &pageSource{id: "source", pages: test.pages}
			got, err := NewDiscoverer().Discover(context.Background(), source)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			if got.Discovery != test.status || len(got.Resources) != test.count {
				t.Fatalf("unexpected aggregate: %+v", got)
			}
			if test.count == 2 && (got.Resources[0].ID != "a" || got.Resources[1].ID != "b") {
				t.Fatalf("resources not canonical: %+v", got.Resources)
			}
		})
	}
}

func TestDiscoverPaginationAndPartial(t *testing.T) {
	unknown := domainmigration.UnknownObservation{Source: "source", Kind: "permission-denied", Scope: "networks", Reason: "read permission absent"}
	source := &pageSource{id: "source", pages: map[string]DiscoveryPage{
		"":     {Status: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{resource("b")}, NextCursor: "next"},
		"next": {Status: domainmigration.DiscoveryPartial, Resources: []domainmigration.Resource{resource("a")}, Unknowns: []domainmigration.UnknownObservation{unknown}},
	}}
	got, err := NewDiscoverer().Discover(context.Background(), source)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got.Discovery != domainmigration.DiscoveryPartial || !reflect.DeepEqual(source.cursors, []string{"", "next"}) {
		t.Fatalf("unexpected pagination result: %+v, cursors=%v", got, source.cursors)
	}
	if len(got.Unknowns) != 1 || got.Unknowns[0] != unknown {
		t.Fatalf("unknown evidence lost: %+v", got.Unknowns)
	}
}

func TestDiscoverPermissionDeniedIsExplicitFailure(t *testing.T) {
	source := &pageSource{id: "source", pages: map[string]DiscoveryPage{}, errs: map[string]error{
		"": &DiscoveryFailure{Kind: DiscoveryPermissionDenied, Scope: "instances", Reason: "read scope denied", Err: errors.New("denied")},
	}}
	got, err := NewDiscoverer().Discover(context.Background(), source)
	if err == nil || !errors.Is(err, source.errs[""]) {
		t.Fatalf("expected preserved permission error, got %v", err)
	}
	if got.Discovery != domainmigration.DiscoveryFailed || len(got.Unknowns) != 1 || got.Unknowns[0].Kind != "permission-denied" || got.Unknowns[0].Scope != "instances" {
		t.Fatalf("permission denial was not explicit: %+v", got)
	}
}

func TestDiscoverRejectsMalformedResults(t *testing.T) {
	tests := []struct {
		name  string
		pages map[string]DiscoveryPage
	}{
		{name: "invalid status", pages: map[string]DiscoveryPage{"": {Status: "invented"}}},
		{name: "partial without unknown", pages: map[string]DiscoveryPage{"": {Status: domainmigration.DiscoveryPartial}}},
		{name: "duplicate resource", pages: map[string]DiscoveryPage{
			"":    {Status: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{resource("a")}, NextCursor: "two"},
			"two": {Status: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{resource("a")}},
		}},
		{name: "repeated cursor", pages: map[string]DiscoveryPage{"": {Status: domainmigration.DiscoveryComplete, NextCursor: "again"}, "again": {Status: domainmigration.DiscoveryComplete, NextCursor: "again"}}},
		{name: "failed without evidence", pages: map[string]DiscoveryPage{"": {Status: domainmigration.DiscoveryFailed}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NewDiscoverer().Discover(context.Background(), &pageSource{id: "source", pages: test.pages})
			if err == nil || got.Discovery != domainmigration.DiscoveryFailed {
				t.Fatalf("expected explicit malformed failure, got aggregate=%+v err=%v", got, err)
			}
			if len(got.Unknowns) == 0 || got.Unknowns[len(got.Unknowns)-1].Kind != "malformed-observation" {
				t.Fatalf("malformed evidence absent: %+v", got.Unknowns)
			}
		})
	}
}

func TestDiscoverCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &pageSource{id: "source", pages: map[string]DiscoveryPage{}}
	_, err := NewDiscoverer().Discover(ctx, source)
	if !errors.Is(err, context.Canceled) || len(source.cursors) != 0 {
		t.Fatalf("cancellation not propagated before provider call: err=%v calls=%v", err, source.cursors)
	}
}

func TestDiscoverProviderOrderHasSameAggregateAndDigest(t *testing.T) {
	one := &pageSource{id: "source", pages: map[string]DiscoveryPage{"": {
		Status: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{resource("b"), resource("a")},
	}}}
	two := &pageSource{id: "source", pages: map[string]DiscoveryPage{"": {
		Status: domainmigration.DiscoveryComplete, Resources: []domainmigration.Resource{resource("a"), resource("b")},
	}}}
	a, err := NewDiscoverer().Discover(context.Background(), one)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewDiscoverer().Discover(context.Background(), two)
	if err != nil {
		t.Fatal(err)
	}
	ad, err := a.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	bd, err := b.ComputeDigest()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) || ad != bd {
		t.Fatalf("provider order changed logical result: a=%+v b=%+v digests=%q/%q", a, b, ad, bd)
	}
}

func TestDiscoverValidatesSourceAndGenericFailure(t *testing.T) {
	for _, source := range []DiscoverySource{nil, &pageSource{id: "", pages: map[string]DiscoveryPage{}}} {
		got, err := NewDiscoverer().Discover(context.Background(), source)
		if err == nil || got.Discovery != domainmigration.DiscoveryFailed {
			t.Fatalf("expected invalid source failure: %+v %v", got, err)
		}
	}
	source := &pageSource{id: "source", pages: map[string]DiscoveryPage{}, errs: map[string]error{"": errors.New("offline")}}
	got, err := NewDiscoverer().Discover(context.Background(), source)
	if err == nil || got.Unknowns[0].Kind != "provider-failure" {
		t.Fatalf("generic provider failure not classified: %+v %v", got, err)
	}
}

func resource(id string) domainmigration.Resource {
	return domainmigration.Resource{ID: id, Kind: "vm", Origin: domainmigration.ResourceOrigin{Source: "source", NativeType: "instance", NativeID: id}}
}

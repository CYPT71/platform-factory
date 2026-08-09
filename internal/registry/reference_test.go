package registry

import "testing"

func TestParseReference(t *testing.T) {
	got, err := ParseReference("docker://registry.example/team/service:v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Registry != "registry.example" || got.Repository != "team/service" || got.Tag != "v1" {
		t.Fatalf("reference=%+v", got)
	}
	latest, err := ParseReference("registry.example/team/service")
	if err != nil || latest.Tag != "latest" {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	for _, invalid := range []string{"", "service:v1", "registry/", "registry/repo:", "registry/repo@sha256:abc", "registry/a/../b:v1"} {
		if _, err := ParseReference(invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

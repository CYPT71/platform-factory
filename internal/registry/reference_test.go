package registry

import (
	"strings"
	"testing"
)

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

func TestParseDigestReference(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	ref, got, err := ParseDigestReference("docker://registry.example/team/service@" + digest)
	if err != nil || got != digest || ref.Registry != "registry.example" || ref.Repository != "team/service" {
		t.Fatalf("ref=%+v digest=%q err=%v", ref, got, err)
	}
	for _, invalid := range []string{"registry.example/team/service:latest", "registry.example/team/service@sha256:short", "registry.example/../service@" + digest, "service@" + digest} {
		if _, _, err := ParseDigestReference(invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestParsePullReference(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	bare, got, err := ParsePullReference("python@" + digest)
	if err != nil || got != digest || bare.Registry != dockerHubRegistry || bare.Repository != "library/python" {
		t.Fatalf("bare=%+v digest=%q err=%v", bare, got, err)
	}
	qualified, got, err := ParsePullReference("registry.example/team/service@" + digest)
	if err != nil || got != digest || qualified.Registry != "registry.example" || qualified.Repository != "team/service" {
		t.Fatalf("qualified=%+v digest=%q err=%v", qualified, got, err)
	}
	for _, invalid := range []string{"python:3.12-slim", "python", "", "registry.example/team/service:latest"} {
		if _, _, err := ParsePullReference(invalid); err == nil {
			t.Fatalf("accepted %q (a pull reference must be digest-pinned)", invalid)
		}
	}
}

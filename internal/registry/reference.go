package registry

import (
	"errors"
	"fmt"
	"strings"
)

// Reference is a fully-qualified OCI registry reference.
type Reference struct {
	Registry   string
	Repository string
	Tag        string
}

// ParseDigestReference accepts only an immutable fully-qualified registry
// reference. It is the read-side counterpart to ParseReference, whose tag-only
// contract prevents accidental mutation by digest during publication.
func ParseDigestReference(value string) (Reference, string, error) {
	value = strings.TrimPrefix(value, "docker://")
	if value == "" || strings.ContainsAny(value, " \t\r\n\x00") {
		return Reference{}, "", errors.New("registry: reference must be non-empty and whitespace-free")
	}
	name, digest, ok := strings.Cut(value, "@")
	if !ok || strings.Contains(digest, "@") || !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		return Reference{}, "", errors.New("registry: immutable reference must end in @sha256:<64 lowercase hex>")
	}
	hex := strings.TrimPrefix(digest, "sha256:")
	for _, char := range hex {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return Reference{}, "", errors.New("registry: immutable reference must end in @sha256:<64 lowercase hex>")
		}
	}
	slash := strings.IndexByte(name, '/')
	if slash <= 0 || slash == len(name)-1 {
		return Reference{}, "", fmt.Errorf("registry: reference %q must include registry/repository", value)
	}
	host, repository := name[:slash], name[slash+1:]
	if repository == "" || strings.HasPrefix(repository, "/") || strings.HasSuffix(repository, "/") || strings.Contains(repository, "..") {
		return Reference{}, "", fmt.Errorf("registry: invalid repository %q", repository)
	}
	return Reference{Registry: host, Repository: repository}, digest, nil
}

// dockerHubRegistry is the default registry host for a bare short name
// with no registry segment at all (e.g. "python") - matches every other
// OCI-aware tool's (docker, podman, buildah) convention for an
// unqualified reference.
const dockerHubRegistry = "registry-1.docker.io"

// ParsePullReference accepts a digest-pinned reference for reading a
// third-party image: registry/repository@sha256:... like
// ParseDigestReference, plus a bare single-segment short name with no
// registry/repository path at all (python@sha256:...), which is expanded
// to registry-1.docker.io/library/<name> - the Docker Hub "official image"
// convention. A reference that already contains a "/" is passed to
// ParseDigestReference unchanged; this function does not attempt to guess
// whether a multi-segment reference's first segment is a registry host or
// a Docker Hub organization, unlike docker/podman's own heuristic - an
// explicit registry-1.docker.io/org/name@sha256:... is required for a
// Docker Hub image under an organization rather than "library". Unlike
// ParseReference (publication, tag-based), this is read-only and always
// requires a digest, never a mutable tag: a base image pulled into a build
// must be pinned and auditable, the same posture pf deploy already
// enforces for the images it applies to a cluster.
func ParsePullReference(value string) (Reference, string, error) {
	trimmed := strings.TrimPrefix(value, "docker://")
	if !strings.Contains(trimmed, "/") {
		trimmed = dockerHubRegistry + "/library/" + trimmed
	}
	return ParseDigestReference(trimmed)
}

// ParseReference accepts registry.example/org/image[:tag]. Publication
// deliberately requires an explicit registry and rejects digest references:
// the digest is derived from the verified local manifest.
func ParseReference(value string) (Reference, error) {
	value = strings.TrimPrefix(value, "docker://")
	if value == "" || strings.ContainsAny(value, " \t\r\n\x00") {
		return Reference{}, errors.New("registry: reference must be non-empty and whitespace-free")
	}
	if strings.Contains(value, "@") {
		return Reference{}, errors.New("registry: publication target must use a tag, not a digest")
	}
	slash := strings.IndexByte(value, '/')
	if slash <= 0 || slash == len(value)-1 {
		return Reference{}, fmt.Errorf("registry: reference %q must include registry/repository", value)
	}
	registry, remainder := value[:slash], value[slash+1:]
	tag := "latest"
	if colon := strings.LastIndexByte(remainder, ':'); colon >= 0 {
		if colon == len(remainder)-1 {
			return Reference{}, errors.New("registry: tag must be non-empty")
		}
		tag, remainder = remainder[colon+1:], remainder[:colon]
	}
	if remainder == "" || strings.HasPrefix(remainder, "/") || strings.HasSuffix(remainder, "/") ||
		strings.Contains(remainder, "..") {
		return Reference{}, fmt.Errorf("registry: invalid repository %q", remainder)
	}
	return Reference{Registry: registry, Repository: remainder, Tag: tag}, nil
}

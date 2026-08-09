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

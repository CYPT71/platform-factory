package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SecretResolver supplies secret values by identity. Values reach a
// stage only through an in-memory tmpfs inside its private mount
// namespace; they never enter cache keys (internal/cache hashes secret
// identities only), stage roots or assembled layouts.
type SecretResolver interface {
	Resolve(ctx context.Context, id string) ([]byte, error)
}

var secretIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// EnvResolver resolves secret IDs from PLATFORM_FACTORY_SECRET_<ID>
// environment variables, with the ID uppercased and dashes mapped to
// underscores.
type EnvResolver struct{}

func (EnvResolver) Resolve(_ context.Context, id string) ([]byte, error) {
	if !secretIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid secret id %q", id)
	}
	name := "PLATFORM_FACTORY_SECRET_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
	value, found := os.LookupEnv(name)
	if !found {
		return nil, fmt.Errorf("secret %q is not set (environment variable %s)", id, name)
	}
	return []byte(value), nil
}

// DirResolver resolves secret IDs from files in a directory. Only
// regular files readable by the current user are accepted.
type DirResolver struct {
	Dir string
}

func (r DirResolver) Resolve(_ context.Context, id string) ([]byte, error) {
	if !secretIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid secret id %q", id)
	}
	name := filepath.Join(r.Dir, id)
	info, err := os.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("secret %q: %w", id, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("secret %q must be a regular file", id)
	}
	return os.ReadFile(name)
}

// redactSecrets replaces every exact secret byte sequence in captured
// output, plus a trailing secret prefix that the capture cap may have
// cut mid-value. This is defense in depth for logs: the primary
// guarantees are the in-memory tmpfs delivery and identity-only cache
// keys.
func redactSecrets(data []byte, secrets [][]byte) []byte {
	if len(secrets) == 0 {
		return data
	}
	replacement := []byte("[secret]")
	for _, secret := range secrets {
		if len(secret) > 0 {
			data = bytes.ReplaceAll(data, secret, replacement)
		}
	}
	for _, secret := range secrets {
		longest := len(secret) - 1
		if longest > len(data) {
			longest = len(data)
		}
		for length := longest; length > 0; length-- {
			if bytes.HasSuffix(data, secret[:length]) {
				data = append(data[:len(data)-length], replacement...)
				break
			}
		}
	}
	return data
}

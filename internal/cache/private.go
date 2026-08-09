package cache

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/CYPT71/secure-oci-base/internal/core"
)

// PrivateStoreAdapter is a dedicated CAS for migration artifacts. It keeps the
// general build cache's sharing modes unchanged while ensuring exported
// provider data is never group/world-readable.
type PrivateStoreAdapter struct {
	*StoreAdapter
	store *Store
}

func OpenPrivateAdapter(root string) (*PrivateStoreAdapter, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	s, err := Open(root)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{s.blobs, s.records, s.leases, filepath.Dir(s.blobs)} {
		if err := os.Chmod(d, 0o700); err != nil {
			return nil, err
		}
	}
	return &PrivateStoreAdapter{StoreAdapter: NewStoreAdapter(s), store: s}, nil
}
func (a *PrivateStoreAdapter) Put(r io.Reader) (core.Descriptor, error) {
	d, err := a.StoreAdapter.Put(r)
	if err != nil {
		return d, err
	}
	hexDigest, err := parseDigest(d.Digest)
	if err != nil {
		return core.Descriptor{}, err
	}
	if err := os.Chmod(a.store.blobPath(hexDigest), 0o600); err != nil {
		return core.Descriptor{}, fmt.Errorf("private cache: chmod blob: %w", err)
	}
	return d, nil
}

var _ core.CacheStore = (*PrivateStoreAdapter)(nil)

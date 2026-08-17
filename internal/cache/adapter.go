// Package cache provides the concrete implementation of core.CacheStore interface.
package cache

import (
	"io"

	"github.com/CYPT71/platform-factory/internal/core"
)

// StoreAdapter wraps a concrete cache.Store to implement the core.CacheStore interface.
// This allows internal/executor to depend only on core.CacheStore instead of the
// concrete internal/cache implementation.
type StoreAdapter struct {
	store *Store
}

// NewStoreAdapter creates a new CacheStore adapter around a concrete Store.
func NewStoreAdapter(store *Store) *StoreAdapter {
	return &StoreAdapter{store: store}
}

// Put streams content into the cache and returns a descriptor.
func (a *StoreAdapter) Put(r io.Reader) (core.Descriptor, error) {
	desc, err := a.store.Put(r)
	return core.Descriptor{
		Digest: desc.Digest,
		Size:   desc.Size,
	}, err
}

// Get retrieves cached content by digest.
func (a *StoreAdapter) Get(digest string) (io.ReadCloser, error) {
	return a.store.Get(digest)
}

// StageKey computes a deterministic cache key for a stage's inputs.
func (a *StoreAdapter) StageKey(inputs core.CacheStageKeyInputs) (string, error) {
	// Convert core.CacheStageKeyInputs to cache.StageKeyInputs
	// They have the same fields, so we can copy directly
	concreteInputs := StageKeyInputs{
		EngineVersion: inputs.EngineVersion,
		Stage:         inputs.Stage,
		BaseDigest:    inputs.BaseDigest,
		InputDigests:  inputs.InputDigests,
		Platform:      inputs.Platform,
	}
	return StageKey(concreteInputs)
}

// OpenAdapter creates a new cache store adapter at the given root.
func OpenAdapter(root string) (*StoreAdapter, error) {
	store, err := Open(root)
	if err != nil {
		return nil, err
	}
	return NewStoreAdapter(store), nil
}

// GetRecord retrieves a cached record by key.
func (a *StoreAdapter) GetRecord(key string, out any) (bool, error) {
	return a.store.GetRecord(key, out)
}

// PutRecord stores a record under a key.
func (a *StoreAdapter) PutRecord(key string, value any) error {
	return a.store.PutRecord(key, value)
}

// Verify checks that a cached blob's content matches its digest.
func (a *StoreAdapter) Verify(digest string) error {
	return a.store.Verify(digest)
}

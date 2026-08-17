// Package core defines the abstract interfaces for Platform Factory's domain.
package core

import (
	"io"
)

// CacheStore is the interface that pipeline stages use to store and retrieve
// content-addressed build artifacts. It abstracts the concrete implementation
// in internal/cache, allowing internal/executor to depend only on this interface.
type CacheStore interface {
	// Put streams content into the cache and returns a descriptor.
	// Identical content is deduplicated.
	Put(r io.Reader) (Descriptor, error)

	// Get retrieves cached content by digest.
	// Returns nil reader if not found.
	Get(digest string) (io.ReadCloser, error)

	// StageKey computes a deterministic cache key for a stage's inputs.
	StageKey(inputs CacheStageKeyInputs) (string, error)

	// GetRecord retrieves a cached record by key.
	GetRecord(key string, out any) (bool, error)

	// PutRecord stores a record under a key.
	PutRecord(key string, value any) error

	// Verify checks that a cached blob's content matches its digest.
	Verify(digest string) error
}

// Descriptor identifies a stored blob by its content digest and size.
type Descriptor struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// CacheStageKeyInputs is the core-owned input to deterministic stage-key
// computation. Concrete cache implementations consume this contract without
// leaking their types back into the domain.
type CacheStageKeyInputs struct {
	EngineVersion string
	Stage         Stage
	BaseDigest    string
	InputDigests  []string
	Platform      string
}

// RecordIndex maps stage keys to their output descriptors.
type RecordIndex map[string]map[string]Descriptor

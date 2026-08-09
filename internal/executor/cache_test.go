package executor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/CYPT71/secure-oci-base/internal/core"
)

// testCacheStore is a consumer-side fake of the core cache port. Concrete
// cache adapter behavior is tested by internal/cache; executor tests only need
// the contract they consume.
type testCacheStore struct {
	mu      sync.Mutex
	blobs   map[string][]byte
	records map[string][]byte
}

func newTestCacheStore() *testCacheStore {
	return &testCacheStore{blobs: map[string][]byte{}, records: map[string][]byte{}}
}

func (s *testCacheStore) Put(reader io.Reader) (core.Descriptor, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return core.Descriptor{}, err
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	s.mu.Lock()
	s.blobs[digest] = append([]byte(nil), data...)
	s.mu.Unlock()
	return core.Descriptor{Digest: digest, Size: int64(len(data))}, nil
}

func (s *testCacheStore) Get(digest string) (io.ReadCloser, error) {
	s.mu.Lock()
	data, ok := s.blobs[digest]
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("blob not found")
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}

func (s *testCacheStore) StageKey(inputs core.CacheStageKeyInputs) (string, error) {
	data, err := json.Marshal(inputs)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (s *testCacheStore) GetRecord(key string, out any) (bool, error) {
	s.mu.Lock()
	data, ok := s.records[key]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(data, out)
}

func (s *testCacheStore) PutRecord(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.records[key] = data
	s.mu.Unlock()
	return nil
}

func (s *testCacheStore) Verify(digest string) error {
	s.mu.Lock()
	data, ok := s.blobs[digest]
	s.mu.Unlock()
	if !ok {
		return errors.New("blob not found")
	}
	sum := sha256.Sum256(data)
	if "sha256:"+hex.EncodeToString(sum[:]) != digest {
		return errors.New("digest mismatch")
	}
	return nil
}

func (s *testCacheStore) evict(digest string) {
	s.mu.Lock()
	delete(s.blobs, digest)
	s.mu.Unlock()
}

func (s *testCacheStore) corrupt(digest string, data []byte) {
	s.mu.Lock()
	s.blobs[digest] = append([]byte(nil), data...)
	s.mu.Unlock()
}

var _ core.CacheStore = (*testCacheStore)(nil)

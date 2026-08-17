// Package cache implements the content-addressed pipeline build cache:
// streamed, atomically installed blobs, stage result records, GC leases and
// garbage collection.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
)

const copyBufferSize = 1 << 20

var (
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	buildIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// Descriptor identifies a stored blob by its content digest and size.
type Descriptor struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// Store is a content-addressed cache rooted at a local directory.
type Store struct {
	blobs   string
	records string
	leases  string
	mu      sync.RWMutex
}

// GCResult reports the outcome of a garbage collection sweep.
type GCResult struct {
	Removed []string
	Bytes   int64
}

// RecordIndex is a verified reconstruction of stage-key records.
type RecordIndex map[string]map[string]Descriptor

// Open creates (if necessary) and returns the cache rooted at root.
func Open(root string) (*Store, error) {
	if root == "" || strings.ContainsRune(root, 0) {
		return nil, errors.New("open cache: root must be non-empty and NUL-free")
	}
	store := &Store{
		blobs:   filepath.Join(root, "blobs", "sha256"),
		records: filepath.Join(root, "records"),
		leases:  filepath.Join(root, "leases"),
	}
	for _, dir := range []string{store.blobs, store.records, store.leases} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("open cache: %w", err)
		}
	}
	return store, nil
}

// Put streams r into the store and returns its content descriptor. Identical
// content already present is deduplicated.
func (s *Store) Put(r io.Reader) (Descriptor, error) {
	temporary, err := os.CreateTemp(s.blobs, ".blob-*")
	if err != nil {
		return Descriptor{}, err
	}
	success := false
	defer func() {
		_ = temporary.Close()
		if !success {
			_ = os.Remove(temporary.Name())
		}
	}()

	hasher := sha256.New()
	written, err := io.CopyBuffer(io.MultiWriter(temporary, hasher), r, make([]byte, copyBufferSize))
	if err != nil {
		return Descriptor{}, err
	}
	if err := temporary.Sync(); err != nil {
		return Descriptor{}, err
	}
	if err := temporary.Close(); err != nil {
		return Descriptor{}, err
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if err := os.Chmod(temporary.Name(), 0444); err != nil {
		return Descriptor{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	finalName := s.blobPath(digest)
	if _, err := os.Stat(finalName); err == nil {
		if err := verifyFile(finalName, digest); err == nil {
			return Descriptor{Digest: "sha256:" + digest, Size: written}, nil
		}
		if err := os.Remove(finalName); err != nil {
			return Descriptor{}, fmt.Errorf("replace corrupt cache blob: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Descriptor{}, err
	}
	if err := os.Rename(temporary.Name(), finalName); err != nil {
		// Another process using the same cache may have installed the same
		// content between Stat and Rename on platforms that do not replace.
		if verifyErr := verifyFile(finalName, digest); verifyErr != nil {
			return Descriptor{}, err
		}
		return Descriptor{Digest: "sha256:" + digest, Size: written}, nil
	}
	success = true
	return Descriptor{Digest: "sha256:" + digest, Size: written}, nil
}

// Get opens the blob identified by digest for reading.
func (s *Store) Get(digest string) (io.ReadCloser, error) {
	hexDigest, err := parseDigest(digest)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return os.Open(s.blobPath(hexDigest))
}

// Exists reports whether digest is present in the store.
func (s *Store) Exists(digest string) (bool, error) {
	hexDigest, err := parseDigest(digest)
	if err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, err := os.Stat(s.blobPath(hexDigest))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

// Verify re-hashes the stored blob and confirms it matches digest.
func (s *Store) Verify(digest string) error {
	hexDigest, err := parseDigest(digest)
	if err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return verifyFile(s.blobPath(hexDigest), hexDigest)
}

func verifyFile(filename, hexDigest string) error {
	reader, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer reader.Close()
	hasher := sha256.New()
	if _, err := io.CopyBuffer(hasher, reader, make([]byte, copyBufferSize)); err != nil {
		return err
	}
	if hexDigest != hex.EncodeToString(hasher.Sum(nil)) {
		return fmt.Errorf("cache: digest mismatch for sha256:%s", hexDigest)
	}
	return nil
}

// PutRecord stores value as JSON under key.
func (s *Store) PutRecord(key string, value any) error {
	name, err := parseKey(key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicWrite(s.records, name+".json", data)
}

// GetRecord loads the record stored under key into out. found is false when
// no record exists for key.
func (s *Store) GetRecord(key string, out any) (bool, error) {
	name, err := parseKey(key)
	if err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(filepath.Join(s.records, name+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteRecord removes a cache/session record. Missing records are accepted so
// cleanup is idempotent after crashes.
func (s *Store) DeleteRecord(key string) error {
	name, err := parseKey(key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = os.Remove(filepath.Join(s.records, name+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// RebuildIndex scans durable records and admits only records whose key is a
// valid digest and whose referenced blobs exist and re-hash correctly.
// Versioned session records are ignored because they are not completed stage
// results. Any corrupt completed record fails the rebuild.
func (s *Store) RebuildIndex() (RecordIndex, error) {
	s.mu.RLock()
	entries, err := os.ReadDir(s.records)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	index := RecordIndex{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		if !digestPattern.MatchString(name) {
			continue
		}
		key := "sha256:" + name
		s.mu.RLock()
		data, readErr := os.ReadFile(filepath.Join(s.records, entry.Name()))
		s.mu.RUnlock()
		if readErr != nil {
			return nil, readErr
		}
		var marker struct {
			APIVersion string `json:"api_version"`
		}
		_ = json.Unmarshal(data, &marker)
		if marker.APIVersion != "" {
			continue
		}
		var record map[string]Descriptor
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("cache: corrupt record %s: %w", key, err)
		}
		for artifact, descriptor := range record {
			if descriptor.Size < 0 {
				return nil, fmt.Errorf("cache: record %s artifact %s has invalid size", key, artifact)
			}
			if err := s.Verify(descriptor.Digest); err != nil {
				return nil, fmt.Errorf("cache: record %s artifact %s: %w", key, artifact, err)
			}
		}
		index[key] = record
	}
	return index, nil
}

// Acquire records that buildID depends on digests, protecting them from GC.
func (s *Store) Acquire(buildID string, digests []string) error {
	if !buildIDPattern.MatchString(buildID) {
		return fmt.Errorf("cache: invalid build id %q", buildID)
	}
	seen := map[string]bool{}
	kept := make([]string, 0, len(digests))
	for _, digest := range digests {
		hexDigest, err := parseDigest(digest)
		if err != nil {
			return err
		}
		normalized := "sha256:" + hexDigest
		if !seen[normalized] {
			seen[normalized] = true
			kept = append(kept, normalized)
		}
	}
	sort.Strings(kept)
	data, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicWrite(s.leases, buildID+".json", data)
}

// Release removes the lease held by buildID, if any.
func (s *Store) Release(buildID string) error {
	if !buildIDPattern.MatchString(buildID) {
		return fmt.Errorf("cache: invalid build id %q", buildID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.leases, buildID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// GC removes blobs that are unreferenced by any active lease and whose
// modification time is older than minAge.
func (s *Store) GC(minAge time.Duration) (GCResult, error) {
	if minAge < 0 {
		return GCResult{}, errors.New("cache: minimum age must not be negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	referenced, err := s.referencedDigests()
	if err != nil {
		return GCResult{}, err
	}
	entries, err := os.ReadDir(s.blobs)
	if err != nil {
		return GCResult{}, err
	}
	cutoff := time.Now().Add(-minAge)
	result := GCResult{}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !digestPattern.MatchString(entry.Name()) || referenced[entry.Name()] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return GCResult{}, err
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.blobs, entry.Name())); err != nil {
			return GCResult{}, err
		}
		result.Removed = append(result.Removed, "sha256:"+entry.Name())
		result.Bytes += info.Size()
	}
	sort.Strings(result.Removed)
	return result, nil
}

func (s *Store) referencedDigests() (map[string]bool, error) {
	entries, err := os.ReadDir(s.leases)
	if err != nil {
		return nil, err
	}
	referenced := map[string]bool{}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.leases, entry.Name()))
		if err != nil {
			return nil, err
		}
		var digests []string
		if err := json.Unmarshal(data, &digests); err != nil {
			return nil, err
		}
		for _, digest := range digests {
			if hexDigest, err := parseDigest(digest); err == nil {
				referenced[hexDigest] = true
			}
		}
	}
	return referenced, nil
}

func (s *Store) blobPath(hexDigest string) string {
	return filepath.Join(s.blobs, hexDigest)
}

func parseDigest(digest string) (string, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) {
		return "", fmt.Errorf("cache: invalid digest %q", digest)
	}
	hexDigest := strings.TrimPrefix(digest, prefix)
	if !digestPattern.MatchString(hexDigest) {
		return "", fmt.Errorf("cache: invalid digest %q", digest)
	}
	return hexDigest, nil
}

func parseKey(key string) (string, error) {
	return parseDigest(key)
}

func atomicWrite(dir, name string, data []byte) error {
	return atomicfile.Write(dir, name, data, 0o644, false)
}

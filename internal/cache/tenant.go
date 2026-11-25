package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var (
	ErrTenantQuotaExceeded = errors.New("cache: tenant disk quota exceeded")
	tenantNamePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
)

// TenantStoreManager provides physically isolated, mode-0700 per-tenant CAS
// directories with optional per-tenant disk byte quotas.
type TenantStoreManager struct {
	root              string
	maxBytesPerTenant int64

	mu     sync.Mutex
	stores map[string]*Store
	// quotaLocks serializes ServeBlob's PUT path (check current usage,
	// then stream and install the new blob) per tenant. Without this,
	// concurrent uploads each read the same stale UsageBytes before any
	// of them is installed, so every one of them can pass the quota
	// check and all get written anyway - a tenant with several
	// non-deduplicatable blobs in flight at once (nothing on this API
	// limits concurrent requests) can install arbitrarily more than its
	// quota permits. GET and Exists never take this lock: they do not
	// change usage, so they cannot race the check they did not make.
	quotaLocks map[string]*sync.Mutex
}

// OpenTenantManager creates a tenant-isolated CAS manager rooted at root.
// maxBytesPerTenant <= 0 disables disk quota enforcement.
func OpenTenantManager(root string, maxBytesPerTenant int64) (*TenantStoreManager, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &TenantStoreManager{
		root:              root,
		maxBytesPerTenant: maxBytesPerTenant,
		stores:            make(map[string]*Store),
		quotaLocks:        make(map[string]*sync.Mutex),
	}, nil
}

// quotaLockFor returns tenantID's own upload serialization lock,
// creating it on first use. tenantID is assumed already validated by a
// prior StoreFor call - this never creates a directory, only a mutex.
func (m *TenantStoreManager) quotaLockFor(tenantID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.quotaLocks[tenantID]
	if !ok {
		lock = &sync.Mutex{}
		m.quotaLocks[tenantID] = lock
	}
	return lock
}

// StoreFor returns the isolated CAS store for tenantID.
func (m *TenantStoreManager) StoreFor(tenantID string) (*Store, error) {
	if !tenantNamePattern.MatchString(tenantID) {
		return nil, fmt.Errorf("cache: invalid tenant id %q", tenantID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.stores[tenantID]; ok {
		return s, nil
	}
	dir := filepath.Join(m.root, "tenants", tenantID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	s, err := Open(dir)
	if err != nil {
		return nil, err
	}
	m.stores[tenantID] = s
	return s, nil
}

// UsageBytes returns total bytes currently stored in tenantID's blob directory.
func (m *TenantStoreManager) UsageBytes(tenantID string) (int64, error) {
	store, err := m.StoreFor(tenantID)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(store.blobs, func(_ string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// ServeBlob handles a GET or PUT /<digest> request scoped strictly to tenantID,
// enforcing per-tenant disk quotas on PUT and guaranteeing zero cross-tenant
// blob visibility.
func (m *TenantStoreManager) ServeBlob(w http.ResponseWriter, r *http.Request, tenantID string, limit int64) {
	if limit <= 0 {
		limit = DefaultRemoteBlobLimit
	}
	store, err := m.StoreFor(tenantID)
	if err != nil {
		http.Error(w, "invalid tenant", http.StatusForbidden)
		return
	}
	digest := strings.TrimPrefix(r.URL.Path, "/")
	if _, err := parseDigest(digest); err != nil || r.URL.RawQuery != "" {
		http.Error(w, "invalid digest", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if err := store.Verify(digest); err != nil {
			http.Error(w, "blob unavailable", http.StatusNotFound)
			return
		}
		reader, err := store.Get(digest)
		if err != nil {
			http.Error(w, "blob unavailable", http.StatusNotFound)
			return
		}
		defer reader.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, reader)
	case http.MethodPut:
		if r.ContentLength < 0 || r.ContentLength > limit {
			http.Error(w, "invalid content length", http.StatusRequestEntityTooLarge)
			return
		}
		// Serializes this tenant's own check-then-install sequence below
		// (see quotaLocks' doc comment) - held for the whole PUT, not
		// just the quota check, since the quota decision is only valid
		// until the matching install actually lands.
		quotaLock := m.quotaLockFor(tenantID)
		quotaLock.Lock()
		defer quotaLock.Unlock()
		if m.maxBytesPerTenant > 0 {
			// Skip quota rejection only if the exact blob already exists for this tenant.
			if store.Verify(digest) != nil {
				used, usageErr := m.UsageBytes(tenantID)
				if usageErr != nil {
					http.Error(w, "quota check unavailable", http.StatusInternalServerError)
					return
				}
				if used+r.ContentLength > m.maxBytesPerTenant {
					http.Error(w, ErrTenantQuotaExceeded.Error(), http.StatusInsufficientStorage)
					return
				}
			}
		}
		temporary, err := os.CreateTemp("", "platform-factory-tenant-cas-*")
		if err != nil {
			http.Error(w, "temporary storage unavailable", http.StatusInternalServerError)
			return
		}
		defer temporary.Close()
		defer os.Remove(temporary.Name())
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(r.Body, limit+1))
		expected := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if copyErr != nil || written != r.ContentLength || written > limit || expected != digest {
			http.Error(w, "content does not match digest or size", http.StatusUnprocessableEntity)
			return
		}
		if _, err := temporary.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "temporary storage unavailable", http.StatusInternalServerError)
			return
		}
		installed, err := store.Put(temporary)
		if err != nil || installed.Digest != digest || installed.Size != written {
			http.Error(w, "CAS installation failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	default:
		w.Header().Set("Allow", "GET, PUT")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

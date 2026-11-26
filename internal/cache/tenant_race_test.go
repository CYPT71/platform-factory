package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestTenantQuotaIsEnforcedUnderConcurrentUploads is a real exploit
// attempt, not a theoretical concern: ServeBlob checks a tenant's current
// UsageBytes, then separately streams and installs the new blob, with no
// lock held across that check-then-write sequence at the tenant level
// (StoreFor's own mutex only protects the stores map; Store.Put's mutex
// only protects one blob's own final install). A hostile tenant with
// several non-deduplicatable blobs in flight at once - trivial to
// arrange, since nothing rate-limits concurrent requests on one
// connection-less HTTP API - can have every one of them pass the quota
// check against the same stale usage reading before any of them is
// actually written, and install all of them anyway.
func TestTenantQuotaIsEnforcedUnderConcurrentUploads(t *testing.T) {
	const quota = 100
	const blobSize = 60
	const concurrentUploads = 8 // 8*60 = 480 bytes >> the 100-byte quota

	manager, err := OpenTenantManager(t.TempDir(), quota)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make([]int, concurrentUploads)
	for i := 0; i < concurrentUploads; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each blob's content must differ, or Store.Put's own
			// content-addressed deduplication (same digest already on
			// disk skips the quota path entirely) would mask the race
			// instead of exercising it.
			payload := bytes.Repeat([]byte{byte(i)}, blobSize)
			sum := sha256.Sum256(payload)
			digest := "sha256:" + hex.EncodeToString(sum[:])
			req := httptest.NewRequest(http.MethodPut, "/"+digest, bytes.NewReader(payload))
			req.ContentLength = int64(len(payload))
			rec := httptest.NewRecorder()
			manager.ServeBlob(rec, req, "tenant-a", DefaultRemoteBlobLimit)
			results[i] = rec.Code
		}(i)
	}
	wg.Wait()

	accepted := 0
	for _, code := range results {
		if code == http.StatusCreated {
			accepted++
		}
	}
	used, err := manager.UsageBytes("tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if used > quota {
		t.Fatalf("tenant quota bypassed under concurrency: %d accepted uploads, %d bytes stored, quota was %d",
			accepted, used, quota)
	}
}

package cache

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const validDigest = "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func openStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return store
}

func TestPutGetExistsRoundTrip(t *testing.T) {
	store := openStore(t)
	desc, err := store.Put(bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if desc.Size != 5 {
		t.Fatalf("size=%d", desc.Size)
	}
	if exists, err := store.Exists(desc.Digest); err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	reader, err := store.Get(desc.Digest)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "hello" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if err := store.Verify(desc.Digest); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestExistsFalseForAbsentDigest(t *testing.T) {
	store := openStore(t)
	exists, err := store.Exists(validDigest)
	if err != nil || exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	if _, err := store.Get(validDigest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("get err=%v", err)
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	store := openStore(t)
	desc, err := store.Put(bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	hexDigest, _ := parseDigest(desc.Digest)
	if err := os.Chmod(store.blobPath(hexDigest), 0644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := os.WriteFile(store.blobPath(hexDigest), []byte("tampered"), 0644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if err := store.Verify(desc.Digest); err == nil {
		t.Fatal("expected verify to detect corruption")
	}
}

func TestPutDeduplicatesIdenticalContent(t *testing.T) {
	store := openStore(t)
	var wg sync.WaitGroup
	descs := make([]Descriptor, 20)
	errs := make([]error, 20)
	for i := range descs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			descs[i], errs[i] = store.Put(bytes.NewReader([]byte("same content")))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("put[%d]: %v", i, err)
		}
		if descs[i].Digest != descs[0].Digest {
			t.Fatalf("digest mismatch: %s vs %s", descs[i].Digest, descs[0].Digest)
		}
	}
	entries, err := os.ReadDir(store.blobs)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one deduplicated blob, got %d", len(entries))
	}
}

// TestPutReplacesCorruptExistingBlob proves that a Put racing (or a prior
// crash leaving) a blob whose content no longer matches its own digest
// path is detected and replaced, not silently trusted as a dedup hit.
func TestPutReplacesCorruptExistingBlob(t *testing.T) {
	store := openStore(t)
	desc, err := store.Put(strings.NewReader("real content"))
	if err != nil {
		t.Fatal(err)
	}
	hexDigest := strings.TrimPrefix(desc.Digest, "sha256:")
	path := store.blobPath(hexDigest)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(strings.NewReader("real content")); err != nil {
		t.Fatalf("put over corrupt existing blob: %v", err)
	}
	if err := store.Verify(desc.Digest); err != nil {
		t.Fatalf("blob was not repaired: %v", err)
	}
}

// TestPutSurfacesRemoveFailureForCorruptExistingBlob forces the rarer
// failure neighboring TestPutReplacesCorruptExistingBlob: the existing
// content at a blob's digest path is corrupt (so it must be replaced) but
// removing it also fails, which must surface as an error rather than a
// silently accepted dedup hit.
func TestPutSurfacesRemoveFailureForCorruptExistingBlob(t *testing.T) {
	store := openStore(t)
	desc, err := store.Put(strings.NewReader("real content"))
	if err != nil {
		t.Fatal(err)
	}
	hexDigest := strings.TrimPrefix(desc.Digest, "sha256:")
	path := store.blobPath(hexDigest)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory at the blob path: os.Stat still finds
	// something there, reading it as a file to verify it fails (EISDIR),
	// and removing a non-empty directory fails too.
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(strings.NewReader("real content")); err == nil {
		t.Fatal("non-empty directory collision at the blob path accepted")
	}
}

type erroringReader struct{}

func (erroringReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestPutLeavesNoResidueOnReadError(t *testing.T) {
	store := openStore(t)
	if _, err := store.Put(erroringReader{}); err == nil {
		t.Fatal("expected error")
	}
	entries, err := os.ReadDir(store.blobs)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no leftover files, got %v", entries)
	}
}

func TestRecordRoundTripAndMissing(t *testing.T) {
	store := openStore(t)
	type payload struct {
		Value string `json:"value"`
	}
	if found, err := store.GetRecord(validDigest, &payload{}); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if err := store.PutRecord(validDigest, payload{Value: "x"}); err != nil {
		t.Fatalf("put record: %v", err)
	}
	var out payload
	found, err := store.GetRecord(validDigest, &out)
	if err != nil || !found || out.Value != "x" {
		t.Fatalf("found=%v err=%v out=%+v", found, err, out)
	}
}

func TestRecordAndKeyRejectPathTraversal(t *testing.T) {
	store := openStore(t)
	for _, key := range []string{
		"../../etc/passwd",
		"sha256:../../../etc/passwd",
		"",
		"not-a-digest",
		"sha256:short",
	} {
		if err := store.PutRecord(key, "x"); err == nil {
			t.Fatalf("PutRecord(%q) accepted an invalid key", key)
		}
		if _, err := store.GetRecord(key, &struct{}{}); err == nil {
			t.Fatalf("GetRecord(%q) accepted an invalid key", key)
		}
		if err := store.DeleteRecord(key); err == nil {
			t.Fatalf("DeleteRecord(%q) accepted an invalid key", key)
		}
	}
}

func TestLeaseAndBuildIDRejectPathTraversal(t *testing.T) {
	store := openStore(t)
	for _, buildID := range []string{"../../etc", "", "Bad_ID", "/abs", "a/b"} {
		if err := store.Acquire(buildID, nil); err == nil {
			t.Fatalf("Acquire(%q) accepted an invalid build id", buildID)
		}
		if err := store.Release(buildID); err == nil {
			t.Fatalf("Release(%q) accepted an invalid build id", buildID)
		}
	}
}

func TestAcquireRejectsInvalidDigestsAndDeduplicates(t *testing.T) {
	store := openStore(t)
	if err := store.Acquire("build-1", []string{"bad"}); err == nil {
		t.Fatal("expected invalid digest rejection")
	}
	if err := store.Acquire("build-1", []string{validDigest, validDigest}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.leases, "build-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(data, []byte(validDigest)) != 1 {
		t.Fatalf("lease was not deduplicated: %s", data)
	}
}

func TestReleaseMissingLeaseIsNoop(t *testing.T) {
	store := openStore(t)
	if err := store.Release("never-acquired"); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestGCRespectsLeasesAndAge(t *testing.T) {
	store := openStore(t)

	leased, err := store.Put(bytes.NewReader([]byte("leased")))
	if err != nil {
		t.Fatalf("put leased: %v", err)
	}
	fresh, err := store.Put(bytes.NewReader([]byte("fresh")))
	if err != nil {
		t.Fatalf("put fresh: %v", err)
	}
	stale, err := store.Put(bytes.NewReader([]byte("stale")))
	if err != nil {
		t.Fatalf("put stale: %v", err)
	}

	if err := store.Acquire("build-1", []string{leased.Digest}); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	oldTime := time.Now().Add(-time.Hour)
	for _, desc := range []Descriptor{leased, stale} {
		hexDigest, _ := parseDigest(desc.Digest)
		if err := os.Chtimes(store.blobPath(hexDigest), oldTime, oldTime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	result, err := store.GC(30 * time.Minute)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != stale.Digest {
		t.Fatalf("removed=%v", result.Removed)
	}
	if result.Bytes != 5 {
		t.Fatalf("bytes=%d", result.Bytes)
	}

	for _, desc := range []Descriptor{leased, fresh} {
		if exists, err := store.Exists(desc.Digest); err != nil || !exists {
			t.Fatalf("expected %s to survive GC: exists=%v err=%v", desc.Digest, exists, err)
		}
	}
	if exists, _ := store.Exists(stale.Digest); exists {
		t.Fatal("expected stale blob to be removed")
	}
}

func TestGCIgnoresNonBlobEntries(t *testing.T) {
	store := openStore(t)
	if err := os.WriteFile(filepath.Join(store.blobs, "not-a-digest"), []byte("x"), 0644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}
	if _, err := store.GC(0); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.blobs, "not-a-digest")); err != nil {
		t.Fatalf("expected stray file to survive GC: %v", err)
	}
}

func TestGCRejectsNegativeAge(t *testing.T) {
	store := openStore(t)
	if _, err := store.GC(-time.Second); err == nil {
		t.Fatal("expected negative age rejection")
	}
}

func TestGetExistsVerifyRejectMalformedDigests(t *testing.T) {
	store := openStore(t)
	for _, digest := range []string{"", "not-a-digest", "sha256:short", "md5:" + validDigest[7:]} {
		if _, err := store.Get(digest); err == nil {
			t.Fatalf("Get(%q) accepted a malformed digest", digest)
		}
		if _, err := store.Exists(digest); err == nil {
			t.Fatalf("Exists(%q) accepted a malformed digest", digest)
		}
		if err := store.Verify(digest); err == nil {
			t.Fatalf("Verify(%q) accepted a malformed digest", digest)
		}
	}
}

func TestVerifyFailsForAbsentDigest(t *testing.T) {
	store := openStore(t)
	if err := store.Verify(validDigest); err == nil {
		t.Fatal("expected verify to fail for an absent blob")
	}
}

func TestPutRecordRejectsUnmarshalableValue(t *testing.T) {
	store := openStore(t)
	if err := store.PutRecord(validDigest, make(chan int)); err == nil {
		t.Fatal("expected PutRecord to reject an unmarshalable value")
	}
}

func TestGetRecordRejectsTypeMismatch(t *testing.T) {
	store := openStore(t)
	if err := store.PutRecord(validDigest, "a string value"); err != nil {
		t.Fatalf("put record: %v", err)
	}
	var out struct{ Value int }
	if _, err := store.GetRecord(validDigest, &out); err == nil {
		t.Fatal("expected GetRecord to reject a type mismatch")
	}
}

func TestAcquireFailsWhenLeaseDirIsUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	store := openStore(t)
	if err := os.Chmod(store.leases, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(store.leases, 0755)
	if err := store.Acquire("build-1", nil); err == nil {
		t.Fatal("expected Acquire to fail when the lease directory is unwritable")
	}
}

func TestGCFailsWhenLeaseFileIsInvalidJSON(t *testing.T) {
	store := openStore(t)
	if err := os.WriteFile(filepath.Join(store.leases, "build-1.json"), []byte("not json"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := store.GC(0); err == nil {
		t.Fatal("expected GC to fail on an invalid lease file")
	}
}

func TestOpenRejectsUncreatableRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(root, []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Open(root); err == nil {
		t.Fatal("expected open to fail when root is a file")
	}
}

func TestOpenRejectsEmptyRoot(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("expected empty root rejection")
	}
}

func TestRebuildIndexVerifiesEveryReferencedBlob(t *testing.T) {
	store := openStore(t)
	descriptor, err := store.Put(strings.NewReader("verified"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRecord(validDigest, map[string]Descriptor{"artifact": descriptor}); err != nil {
		t.Fatal(err)
	}
	index, err := store.RebuildIndex()
	if err != nil || index[validDigest]["artifact"].Digest != descriptor.Digest {
		t.Fatalf("index=%+v err=%v", index, err)
	}
	hexDigest := strings.TrimPrefix(descriptor.Digest, "sha256:")
	if err := os.Chmod(store.blobPath(hexDigest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.blobPath(hexDigest), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RebuildIndex(); err == nil {
		t.Fatal("corrupt referenced blob was admitted")
	}
}

func TestRebuildIndexRejectsCorruptRecordShape(t *testing.T) {
	store := openStore(t)
	name := strings.TrimPrefix(validDigest, "sha256:") + ".json"
	if err := os.WriteFile(filepath.Join(store.records, name), []byte(`"not an object"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RebuildIndex(); err == nil {
		t.Fatal("record with the wrong JSON shape was admitted")
	}
}

func TestRebuildIndexRejectsNegativeArtifactSize(t *testing.T) {
	store := openStore(t)
	if err := store.PutRecord(validDigest, map[string]Descriptor{
		"artifact": {Digest: validDigest, Size: -1},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RebuildIndex(); err == nil {
		t.Fatal("record with a negative artifact size was admitted")
	}
}

type diskFullReader struct{ delivered bool }

func (r *diskFullReader) Read(p []byte) (int, error) {
	if r.delivered {
		return 0, syscall.ENOSPC
	}
	r.delivered = true
	return copy(p, []byte("partial")), syscall.ENOSPC
}

func TestPutCleansPartialBlobAfterDiskFull(t *testing.T) {
	store := openStore(t)
	if _, err := store.Put(&diskFullReader{}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("err=%v", err)
	}
	entries, err := os.ReadDir(store.blobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial blobs remain after ENOSPC: %v", entries)
	}
}

func TestPutFailsWhenBlobsDirIsUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	store := openStore(t)
	if err := os.Chmod(store.blobs, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(store.blobs, 0o755)
	if _, err := store.Put(bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("expected Put to fail when the blobs directory is unwritable")
	}
}

func TestGetRecordSurfacesReadFailureOtherThanMissing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	store := openStore(t)
	if err := store.PutRecord(validDigest, "value"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.records, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(store.records, 0o755)
	if _, err := store.GetRecord(validDigest, &struct{}{}); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected a non-missing read failure, got %v", err)
	}
}

func TestDeleteRecordSurfacesPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	store := openStore(t)
	if err := store.PutRecord(validDigest, "value"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.records, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(store.records, 0o755)
	if err := store.DeleteRecord(validDigest); err == nil {
		t.Fatal("expected DeleteRecord to fail when the records directory is unwritable")
	}
}

func TestRebuildIndexSurfacesReadDirFailure(t *testing.T) {
	store := openStore(t)
	if err := os.RemoveAll(store.records); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RebuildIndex(); err == nil {
		t.Fatal("expected RebuildIndex to fail when the records directory is gone")
	}
}

func TestRebuildIndexSurfacesPerRecordReadFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	store := openStore(t)
	if err := store.PutRecord(validDigest, map[string]Descriptor{}); err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(validDigest, "sha256:") + ".json"
	recordPath := filepath.Join(store.records, name)
	if err := os.Chmod(recordPath, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(recordPath, 0o644)
	if _, err := store.RebuildIndex(); err == nil {
		t.Fatal("expected RebuildIndex to fail when a record file is unreadable")
	}
}

func TestGCIgnoresNonJSONLeaseEntries(t *testing.T) {
	store := openStore(t)
	if err := os.WriteFile(filepath.Join(store.leases, "not-a-lease"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}
	if _, err := store.GC(0); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.leases, "not-a-lease")); err != nil {
		t.Fatalf("expected stray lease file to survive GC: %v", err)
	}
}

func TestGCSurfacesBlobsReadDirFailure(t *testing.T) {
	store := openStore(t)
	if err := os.RemoveAll(store.blobs); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GC(0); err == nil {
		t.Fatal("expected GC to fail when the blobs directory is gone")
	}
}

func TestGCSurfacesRemoveFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	store := openStore(t)
	stale, err := store.Put(bytes.NewReader([]byte("stale")))
	if err != nil {
		t.Fatal(err)
	}
	hexDigest, _ := parseDigest(stale.Digest)
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(store.blobPath(hexDigest), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.blobs, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(store.blobs, 0o755)
	if _, err := store.GC(30 * time.Minute); err == nil {
		t.Fatal("expected GC to fail when it cannot remove an eligible blob")
	}
}

func TestGCSurfacesLeaseReadFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	store := openStore(t)
	if err := store.Acquire("build-1", nil); err != nil {
		t.Fatal(err)
	}
	leaseFile := filepath.Join(store.leases, "build-1.json")
	if err := os.Chmod(leaseFile, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(leaseFile, 0o644)
	if _, err := store.GC(0); err == nil {
		t.Fatal("expected GC to fail when a lease file is unreadable")
	}
}

// TestPutRecordFailsWhenDestinationPathIsOccupied mirrors
// TestPutSurfacesRemoveFailureForCorruptExistingBlob's technique: a
// directory sitting at the record's destination path makes the final
// os.Rename inside atomicWrite fail, and that failure (plus the temp-file
// cleanup it triggers) must propagate rather than being silently absorbed.
func TestPutRecordFailsWhenDestinationPathIsOccupied(t *testing.T) {
	store := openStore(t)
	name := strings.TrimPrefix(validDigest, "sha256:") + ".json"
	if err := os.MkdirAll(filepath.Join(store.records, name, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRecord(validDigest, "x"); err == nil {
		t.Fatal("expected PutRecord to fail when the destination path is a directory")
	}
}

func TestRebuildIndexIgnoresCrashTemporaryFiles(t *testing.T) {
	store := openStore(t)
	if err := os.WriteFile(filepath.Join(store.blobs, ".blob-crashed"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.records, ".record-crashed"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := store.RebuildIndex()
	if err != nil || len(index) != 0 {
		t.Fatalf("index=%v err=%v", index, err)
	}
}

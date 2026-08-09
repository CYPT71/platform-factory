package cache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// fakeContentStore lets tests inject failures from any ContentStore method
// without a real Store, for branches a real Store cannot easily be made to
// fail on demand (a corrupt manifest, a store that refuses every write).
type fakeContentStore struct {
	putErr, getErr, verifyErr, existsErr error
	exists                               bool
	getData                              []byte
}

func (s *fakeContentStore) Put(io.Reader) (Descriptor, error) {
	if s.putErr != nil {
		return Descriptor{}, s.putErr
	}
	return Descriptor{Digest: "sha256:" + strings.Repeat("a", 64), Size: int64(len(s.getData))}, nil
}
func (s *fakeContentStore) Get(string) (io.ReadCloser, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return io.NopCloser(bytes.NewReader(s.getData)), nil
}
func (s *fakeContentStore) Exists(string) (bool, error) { return s.exists, s.existsErr }
func (s *fakeContentStore) Verify(string) error         { return s.verifyErr }

type virtualReader struct{ remaining int64 }

func (r *virtualReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(buffer))
	if n > r.remaining {
		n = r.remaining
	}
	r.remaining -= n
	// The corpus is logically zero-filled. Avoid physically touching n bytes:
	// PutChunked's bounded buffer is zero-initialized once.
	return int(n), nil
}

type countingContentStore struct {
	puts int
}

func (s *countingContentStore) Put(io.Reader) (Descriptor, error) {
	s.puts++
	return Descriptor{
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:   64 << 20,
	}, nil
}
func (*countingContentStore) Get(string) (io.ReadCloser, error) { return nil, io.EOF }
func (*countingContentStore) Exists(string) (bool, error)       { return true, nil }
func (*countingContentStore) Verify(string) error               { return nil }

type interruptReader struct {
	data []byte
	done bool
}

func (r *interruptReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.ErrClosedPipe
	}
	r.done = true
	n := copy(p, r.data)
	return n, io.ErrClosedPipe
}

func TestChunkedRoundTripAndDeduplication(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("same"), 8)
	root, manifest, err := PutChunked(context.Background(), store, bytes.NewReader(payload), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chunks) != 4 || manifest.Size != int64(len(payload)) {
		t.Fatalf("manifest=%+v", manifest)
	}
	first := manifest.Chunks[0].Digest
	for _, chunk := range manifest.Chunks {
		if chunk.Digest != first {
			t.Fatalf("identical chunks were not deduplicated: %+v", manifest.Chunks)
		}
	}
	reader, decoded, err := OpenChunked(store, root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) || decoded.Size != int64(len(payload)) {
		t.Fatalf("round trip mismatch")
	}
}

func TestPutChunkedCancellation(t *testing.T) {
	store, _ := Open(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := PutChunked(ctx, store, bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestPutChunkedRejectsNilStore(t *testing.T) {
	if _, _, err := PutChunked(context.Background(), nil, bytes.NewReader(nil), 1); err == nil {
		t.Fatal("nil store accepted")
	}
}

func TestPutChunkedSurfacesStoreAndReadFailures(t *testing.T) {
	if _, _, err := PutChunked(context.Background(), &fakeContentStore{putErr: errors.New("write refused")},
		bytes.NewReader([]byte("chunk")), 1); err == nil {
		t.Fatal("chunk store failure accepted")
	}
	if _, _, err := PutChunked(context.Background(), &fakeContentStore{},
		erroringReader{}, 4); err == nil {
		t.Fatal("read failure accepted")
	}
}

func TestOpenChunkedSurfacesFailures(t *testing.T) {
	root := Descriptor{Digest: "sha256:" + strings.Repeat("a", 64)}
	if _, _, err := OpenChunked(&fakeContentStore{verifyErr: errors.New("corrupt")}, root); err == nil {
		t.Fatal("manifest verify failure accepted")
	}
	if _, _, err := OpenChunked(&fakeContentStore{getErr: errors.New("missing")}, root); err == nil {
		t.Fatal("manifest fetch failure accepted")
	}
	if _, _, err := OpenChunked(&fakeContentStore{getData: []byte("not json")}, root); err == nil {
		t.Fatal("malformed manifest accepted")
	}
	if _, _, err := OpenChunked(&fakeContentStore{getData: []byte(`{"api_version":"wrong"}`)}, root); err == nil {
		t.Fatal("wrong manifest api_version accepted")
	}
	if _, _, err := OpenChunked(&fakeContentStore{
		getData: []byte(`{"api_version":"secure-oci.dev/chunks/v1","chunk_size":0}`),
	}, root); err == nil {
		t.Fatal("zero chunk_size accepted")
	}
}

func TestChunkReaderSurfacesPerChunkFailures(t *testing.T) {
	chunk := Descriptor{Digest: "sha256:" + strings.Repeat("b", 64)}
	verifyFails := &chunkReader{store: &fakeContentStore{verifyErr: errors.New("corrupt chunk")}, chunks: []Descriptor{chunk}}
	if _, err := verifyFails.Read(make([]byte, 8)); err == nil {
		t.Fatal("chunk verify failure accepted")
	}
	getFails := &chunkReader{store: &fakeContentStore{getErr: errors.New("missing chunk")}, chunks: []Descriptor{chunk}}
	if _, err := getFails.Read(make([]byte, 8)); err == nil {
		t.Fatal("chunk fetch failure accepted")
	}
	empty := &chunkReader{store: &fakeContentStore{}}
	if _, err := empty.Read(make([]byte, 8)); !errors.Is(err, io.EOF) {
		t.Fatalf("empty chunk reader err=%v, want io.EOF", err)
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("close with no open chunk: %v", err)
	}
}

func TestOpenChunkSessionRejectsInvalidArguments(t *testing.T) {
	store, _ := Open(t.TempDir())
	if _, err := OpenChunkSession(nil, "session-1", 1); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := OpenChunkSession(store, "Not Valid!", 1); err == nil {
		t.Fatal("invalid session id accepted")
	}
}

func TestOpenChunkSessionRejectsIncompatibleResumedSession(t *testing.T) {
	store, _ := Open(t.TempDir())
	if _, err := OpenChunkSession(store, "session-1", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenChunkSession(store, "session-1", 8); err == nil {
		t.Fatal("resuming with a different chunk_size was accepted")
	}
}

func TestOpenChunkSessionRejectsMissingResumedChunk(t *testing.T) {
	store, _ := Open(t.TempDir())
	session, err := OpenChunkSession(store, "session-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Append(context.Background(), bytes.NewReader([]byte("data"))); err != nil {
		t.Fatal(err)
	}
	chunk := session.record.Chunks[0]
	hexDigest := strings.TrimPrefix(chunk.Digest, "sha256:")
	if err := os.Remove(store.blobPath(hexDigest)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenChunkSession(store, "session-1", 4); err == nil {
		t.Fatal("resumed session with a missing chunk blob was accepted")
	}
}

func TestChunkSessionAppendSurfacesNonEOFReadError(t *testing.T) {
	store, _ := Open(t.TempDir())
	session, err := OpenChunkSession(store, "session-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Append(context.Background(), erroringReader{}); err == nil {
		t.Fatal("non-EOF read failure accepted")
	}
}

func TestChunkSessionAppendRejectsCanceledContext(t *testing.T) {
	store, _ := Open(t.TempDir())
	session, err := OpenChunkSession(store, "session-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.Append(ctx, bytes.NewReader([]byte("data"))); err == nil {
		t.Fatal("canceled context accepted")
	}
}

func TestChunkSessionResumesAtLastCompleteChunk(t *testing.T) {
	store, _ := Open(t.TempDir())
	session, err := OpenChunkSession(store, "large-image", 4)
	if err != nil {
		t.Fatal(err)
	}
	// One complete chunk and two unacknowledged bytes arrive before failure.
	if err := session.Append(context.Background(), io.MultiReader(
		bytes.NewReader([]byte("abcd")), &interruptReader{data: []byte("ef")})); err == nil {
		t.Fatal("expected interruption")
	}
	if session.Offset() != 4 {
		t.Fatalf("offset=%d want=4", session.Offset())
	}
	resumed, err := OpenChunkSession(store, "large-image", 4)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Offset() != 4 {
		t.Fatalf("resumed offset=%d", resumed.Offset())
	}
	if err := resumed.Append(context.Background(), bytes.NewReader([]byte("efghij"))); err != nil {
		t.Fatal(err)
	}
	root, manifest, err := resumed.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	reader, _, err := OpenChunked(store, root)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(reader)
	_ = reader.Close()
	if string(got) != "abcdefghij" || manifest.Size != 10 {
		t.Fatalf("got=%q manifest=%+v", got, manifest)
	}
}

func TestPutChunkedDefaultsInvalidChunkSize(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := PutChunked(context.Background(), store, bytes.NewReader([]byte("x")), 0)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ChunkSize != DefaultChunkSize {
		t.Fatalf("chunk size=%d, want default %d", manifest.ChunkSize, DefaultChunkSize)
	}
}

// TestPutChunkedSurfacesManifestStoreFailure drives an empty input, so
// PutChunked never stores a data chunk (n stays 0 every read) and the only
// store.Put call is for the manifest itself; that call failing must
// propagate as a distinct error from a per-chunk store failure.
func TestPutChunkedSurfacesManifestStoreFailure(t *testing.T) {
	store := &fakeContentStore{putErr: errors.New("manifest store refused")}
	if _, _, err := PutChunked(context.Background(), store, bytes.NewReader(nil), 4); err == nil {
		t.Fatal("expected manifest store failure to propagate")
	}
}

type eofWithDataReader struct {
	data []byte
	done bool
}

func (r *eofWithDataReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, r.data)
	return n, io.EOF
}
func (r *eofWithDataReader) Close() error { return nil }

type singleChunkStore struct{ reader *eofWithDataReader }

func (s *singleChunkStore) Put(io.Reader) (Descriptor, error) { return Descriptor{}, nil }
func (s *singleChunkStore) Get(string) (io.ReadCloser, error) { return s.reader, nil }
func (s *singleChunkStore) Exists(string) (bool, error)       { return true, nil }
func (s *singleChunkStore) Verify(string) error               { return nil }

// TestChunkReaderHandlesCombinedDataAndEOF proves chunkReader.Read does not
// drop the final bytes of a chunk when the underlying reader signals EOF in
// the same call that delivers data (as some real readers do).
func TestChunkReaderHandlesCombinedDataAndEOF(t *testing.T) {
	store := &singleChunkStore{reader: &eofWithDataReader{data: []byte("payload")}}
	reader := &chunkReader{store: store, chunks: []Descriptor{{Digest: "sha256:" + strings.Repeat("c", 64)}}}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Fatalf("got=%q, want payload", got)
	}
}

type trackingCloser struct {
	closed bool
	err    error
}

func (c *trackingCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (c *trackingCloser) Close() error {
	c.closed = true
	return c.err
}

// TestChunkReaderCloseDelegatesToCurrentChunk proves Close on a chunkReader
// mid-stream closes (and surfaces the error of) the currently open chunk,
// not just the no-op case already covered when nothing is open.
func TestChunkReaderCloseDelegatesToCurrentChunk(t *testing.T) {
	closer := &trackingCloser{err: errors.New("close failed")}
	reader := &chunkReader{current: closer}
	if err := reader.Close(); err != closer.err {
		t.Fatalf("err=%v, want %v", err, closer.err)
	}
	if !closer.closed {
		t.Fatal("expected the underlying chunk reader to be closed")
	}
}

// TestChunkSessionAppendSurfacesCheckpointPutFailure forces the blob store
// itself to refuse the write inside checkpoint, and asserts the session's
// durable offset is not advanced when that happens.
func TestChunkSessionAppendSurfacesCheckpointPutFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := OpenChunkSession(store, "session-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.blobs, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(store.blobs, 0o755)
	if err := session.Append(context.Background(), bytes.NewReader([]byte("abcd"))); err == nil {
		t.Fatal("expected checkpoint store failure to propagate")
	}
	if session.Offset() != 0 {
		t.Fatalf("offset=%d, want 0 after a failed checkpoint", session.Offset())
	}
}

// TestChunkSessionAppendSurfacesCheckpointRecordFailure forces the session
// record write (not the blob write) to fail, and asserts checkpoint rolls
// back the in-memory chunk list and size rather than acknowledging bytes
// that were not durably recorded.
func TestChunkSessionAppendSurfacesCheckpointRecordFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := OpenChunkSession(store, "session-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.records, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(store.records, 0o755)
	if err := session.Append(context.Background(), bytes.NewReader([]byte("abcd"))); err == nil {
		t.Fatal("expected record checkpoint failure to propagate")
	}
	if session.Offset() != 0 {
		t.Fatalf("offset=%d, want 0 after a rolled-back checkpoint", session.Offset())
	}
}

type unexpectedEOFReader struct{}

func (unexpectedEOFReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// TestChunkSessionAppendToleratesEmptyUnexpectedEOF covers the defensive
// zero-byte io.ErrUnexpectedEOF branch: a reader that reports
// ErrUnexpectedEOF directly (rather than io.ReadFull synthesizing it from a
// short read) with no bytes at all must be treated as a clean, empty append.
func TestChunkSessionAppendToleratesEmptyUnexpectedEOF(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := OpenChunkSession(store, "session-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Append(context.Background(), unexpectedEOFReader{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.Offset() != 0 {
		t.Fatalf("offset=%d, want 0", session.Offset())
	}
}

// TestChunkSessionFinalizeSurfacesStoreFailure forces the manifest blob
// write inside Finalize to fail after a chunk was already durably appended.
func TestChunkSessionFinalizeSurfacesStoreFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := OpenChunkSession(store, "session-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Append(context.Background(), bytes.NewReader([]byte("abcd"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.blobs, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(store.blobs, 0o755)
	if _, _, err := session.Finalize(); err == nil {
		t.Fatal("expected manifest store failure to propagate")
	}
}

// TestChunkSessionFinalizeSurfacesDeleteRecordFailure forces the session
// record cleanup at the end of Finalize to fail, even though the manifest
// blob itself was written successfully.
func TestChunkSessionFinalizeSurfacesDeleteRecordFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := OpenChunkSession(store, "session-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Append(context.Background(), bytes.NewReader([]byte("abcd"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.records, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(store.records, 0o755)
	if _, _, err := session.Finalize(); err == nil {
		t.Fatal("expected delete-record failure to propagate")
	}
}

func TestMultiTiBCorpusUsesBoundedChunksAndDeduplication(t *testing.T) {
	const (
		corpusSize = int64(2) << 40
		chunkSize  = 64 << 20
	)
	store := &countingContentStore{}
	_, manifest, err := PutChunked(context.Background(), store,
		&virtualReader{remaining: corpusSize}, chunkSize)
	if err != nil {
		t.Fatal(err)
	}
	wantChunks := int(corpusSize / chunkSize)
	if manifest.Size != corpusSize || len(manifest.Chunks) != wantChunks {
		t.Fatalf("size=%d chunks=%d want=%d", manifest.Size, len(manifest.Chunks), wantChunks)
	}
	if store.puts != wantChunks+1 { // chunks plus the root manifest
		t.Fatalf("puts=%d want=%d", store.puts, wantChunks+1)
	}
	first := manifest.Chunks[0].Digest
	for _, chunk := range manifest.Chunks {
		if chunk.Digest != first {
			t.Fatal("zero-filled chunks did not deduplicate to one digest")
		}
	}
}

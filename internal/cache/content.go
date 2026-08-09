package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

const DefaultChunkSize = 4 << 20

var sessionIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ContentStore is the stable storage boundary used by pipeline, registry and
// future distributed workers. Implementations must verify content addresses
// and install writes atomically.
type ContentStore interface {
	Put(io.Reader) (Descriptor, error)
	Get(string) (io.ReadCloser, error)
	Exists(string) (bool, error)
	Verify(string) error
}

// ChunkManifest describes a large logical object as independently verified,
// deduplicated chunks. Its own canonical JSON bytes are also content-addressed.
type ChunkManifest struct {
	APIVersion string       `json:"api_version"`
	Size       int64        `json:"size"`
	ChunkSize  int          `json:"chunk_size"`
	Chunks     []Descriptor `json:"chunks"`
}

// PutChunked streams an arbitrarily large input into fixed-size chunks. Peak
// payload memory is bounded by chunkSize and repeated chunks are deduplicated
// by Store.Put. The returned descriptor identifies the manifest, not a
// concatenated copy of the input.
func PutChunked(ctx context.Context, store ContentStore, reader io.Reader, chunkSize int) (Descriptor, ChunkManifest, error) {
	if store == nil {
		return Descriptor{}, ChunkManifest{}, errors.New("cache: content store is required")
	}
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	manifest := ChunkManifest{APIVersion: "platform-factory.dev/chunks/v1", ChunkSize: chunkSize}
	buffer := make([]byte, chunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return Descriptor{}, ChunkManifest{}, err
		}
		n, err := io.ReadFull(reader, buffer)
		if n > 0 {
			descriptor, putErr := store.Put(bytes.NewReader(buffer[:n]))
			if putErr != nil {
				return Descriptor{}, ChunkManifest{}, fmt.Errorf("cache: store chunk %d: %w", len(manifest.Chunks), putErr)
			}
			manifest.Chunks = append(manifest.Chunks, descriptor)
			manifest.Size += int64(n)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return Descriptor{}, ChunkManifest{}, fmt.Errorf("cache: read chunk: %w", err)
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return Descriptor{}, ChunkManifest{}, err
	}
	root, err := store.Put(bytes.NewReader(encoded))
	if err != nil {
		return Descriptor{}, ChunkManifest{}, fmt.Errorf("cache: store chunk manifest: %w", err)
	}
	return root, manifest, nil
}

// OpenChunked validates a stored chunk manifest and returns a streaming reader
// that concatenates its verified chunks without materializing the object.
func OpenChunked(store ContentStore, root Descriptor) (io.ReadCloser, ChunkManifest, error) {
	if err := store.Verify(root.Digest); err != nil {
		return nil, ChunkManifest{}, fmt.Errorf("cache: verify chunk manifest: %w", err)
	}
	file, err := store.Get(root.Digest)
	if err != nil {
		return nil, ChunkManifest{}, err
	}
	var manifest ChunkManifest
	decodeErr := json.NewDecoder(io.LimitReader(file, 16<<20)).Decode(&manifest)
	_ = file.Close()
	if decodeErr != nil {
		return nil, ChunkManifest{}, fmt.Errorf("cache: decode chunk manifest: %w", decodeErr)
	}
	// secure-oci.dev/chunks/v1 is the pre-rebrand identifier: a chunk
	// manifest is content-addressed and can persist in the CAS
	// indefinitely, unlike the short-lived session record above, so it's
	// still accepted for the documented compatibility overlap window
	// (see docs/api-compatibility.md).
	if (manifest.APIVersion != "platform-factory.dev/chunks/v1" && manifest.APIVersion != "secure-oci.dev/chunks/v1") || manifest.ChunkSize <= 0 {
		return nil, ChunkManifest{}, errors.New("cache: invalid chunk manifest")
	}
	return &chunkReader{store: store, chunks: manifest.Chunks}, manifest, nil
}

type chunkReader struct {
	store   ContentStore
	chunks  []Descriptor
	index   int
	current io.ReadCloser
}

func (r *chunkReader) Read(buffer []byte) (int, error) {
	for {
		if r.current == nil {
			if r.index >= len(r.chunks) {
				return 0, io.EOF
			}
			next := r.chunks[r.index]
			if err := r.store.Verify(next.Digest); err != nil {
				return 0, err
			}
			file, err := r.store.Get(next.Digest)
			if err != nil {
				return 0, err
			}
			r.current = file
			r.index++
		}
		n, err := r.current.Read(buffer)
		if errors.Is(err, io.EOF) {
			_ = r.current.Close()
			r.current = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (r *chunkReader) Close() error {
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}

var _ ContentStore = (*Store)(nil)

type chunkSessionRecord struct {
	APIVersion string       `json:"api_version"`
	ID         string       `json:"id"`
	ChunkSize  int          `json:"chunk_size"`
	Size       int64        `json:"size"`
	Chunks     []Descriptor `json:"chunks"`
}

// ChunkSession is a crash-resumable large-object ingestion. Offset is the
// number of source bytes durably accepted; callers seek their source to that
// offset after reopening a session. Only complete chunks are checkpointed, so
// an interrupted partial chunk is resent and chunk boundaries remain
// deterministic.
type ChunkSession struct {
	store  *Store
	record chunkSessionRecord
	key    string
}

func OpenChunkSession(store *Store, id string, chunkSize int) (*ChunkSession, error) {
	if store == nil || !sessionIDPattern.MatchString(id) {
		return nil, errors.New("cache: valid store and chunk session id are required")
	}
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	keySum := sha256.Sum256([]byte("chunk-session:" + id))
	key := "sha256:" + hex.EncodeToString(keySum[:])
	record := chunkSessionRecord{}
	found, err := store.GetRecord(key, &record)
	if err != nil {
		return nil, fmt.Errorf("cache: load chunk session: %w", err)
	}
	if found {
		if record.APIVersion != "platform-factory.dev/chunk-session/v1" || record.ID != id || record.ChunkSize != chunkSize {
			return nil, errors.New("cache: incompatible or corrupt chunk session")
		}
		for _, chunk := range record.Chunks {
			exists, err := store.Exists(chunk.Digest)
			if err != nil || !exists {
				return nil, fmt.Errorf("cache: resumed chunk %s is missing", chunk.Digest)
			}
		}
	} else {
		record = chunkSessionRecord{
			APIVersion: "platform-factory.dev/chunk-session/v1", ID: id, ChunkSize: chunkSize,
		}
		if err := store.PutRecord(key, record); err != nil {
			return nil, err
		}
	}
	return &ChunkSession{store: store, record: record, key: key}, nil
}

func (s *ChunkSession) Offset() int64 { return s.record.Size }

// Append consumes full chunks until EOF. A non-EOF read error checkpoints all
// preceding chunks and returns; bytes from the incomplete chunk are not
// acknowledged and must be resent from Offset.
func (s *ChunkSession) Append(ctx context.Context, reader io.Reader) error {
	buffer := make([]byte, s.record.ChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(reader, buffer)
		switch {
		case err == nil:
			if putErr := s.checkpoint(buffer[:n]); putErr != nil {
				return putErr
			}
		case errors.Is(err, io.EOF):
			return nil
		case errors.Is(err, io.ErrUnexpectedEOF):
			if n > 0 {
				return s.checkpoint(buffer[:n])
			}
			return nil
		default:
			// Do not acknowledge n bytes from a failed partial read.
			return fmt.Errorf("cache: append chunk session at offset %d: %w", s.record.Size, err)
		}
	}
}

func (s *ChunkSession) checkpoint(payload []byte) error {
	descriptor, err := s.store.Put(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	s.record.Chunks = append(s.record.Chunks, descriptor)
	s.record.Size += int64(len(payload))
	if err := s.store.PutRecord(s.key, s.record); err != nil {
		s.record.Chunks = s.record.Chunks[:len(s.record.Chunks)-1]
		s.record.Size -= int64(len(payload))
		return err
	}
	return nil
}

func (s *ChunkSession) Finalize() (Descriptor, ChunkManifest, error) {
	manifest := ChunkManifest{
		APIVersion: "platform-factory.dev/chunks/v1", Size: s.record.Size,
		ChunkSize: s.record.ChunkSize, Chunks: append([]Descriptor(nil), s.record.Chunks...),
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return Descriptor{}, ChunkManifest{}, err
	}
	root, err := s.store.Put(bytes.NewReader(encoded))
	if err != nil {
		return Descriptor{}, ChunkManifest{}, err
	}
	if err := s.store.DeleteRecord(s.key); err != nil {
		return Descriptor{}, ChunkManifest{}, err
	}
	return root, manifest, nil
}

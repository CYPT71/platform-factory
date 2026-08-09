package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

type replicaStore struct {
	data          []byte
	verifyErr     error
	exists        bool
	putDescriptor *Descriptor
}

func (s *replicaStore) Put(reader io.Reader) (Descriptor, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return Descriptor{}, err
	}
	s.data = data
	s.exists = true
	if s.putDescriptor != nil {
		return *s.putDescriptor, nil
	}
	digest := sha256.Sum256(data)
	return Descriptor{Digest: "sha256:" + hex.EncodeToString(digest[:]), Size: int64(len(data))}, nil
}

func (s *replicaStore) Get(string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func (s *replicaStore) Exists(string) (bool, error) { return s.exists, nil }
func (s *replicaStore) Verify(string) error         { return s.verifyErr }

func TestReplicateVerifiesBothStores(t *testing.T) {
	source, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("verified distributed cache payload")
	descriptor, err := source.Put(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if err := Replicate(context.Background(), source, destination, descriptor); err != nil {
		t.Fatal(err)
	}
	if err := Replicate(context.Background(), source, destination, descriptor); err != nil {
		t.Fatalf("idempotent replication: %v", err)
	}
	reader, err := destination.Get(descriptor.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("payload=%q err=%v", got, err)
	}
}

func TestReplicateRejectsUnverifiedOrMalformedContent(t *testing.T) {
	source, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := source.Put(bytes.NewReader([]byte("payload")))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		ctx         context.Context
		source      ContentStore
		destination ContentStore
		descriptor  Descriptor
		want        string
	}{
		{name: "nil source", ctx: context.Background(), destination: source, descriptor: descriptor, want: "required"},
		{name: "negative size", ctx: context.Background(), source: source, destination: source,
			descriptor: Descriptor{Digest: descriptor.Digest, Size: -1}, want: "negative"},
		{name: "malformed digest", ctx: context.Background(), source: source, destination: source,
			descriptor: Descriptor{Digest: "sha256:short"}, want: "invalid"},
		{name: "corrupt source", ctx: context.Background(),
			source:      &replicaStore{data: []byte("payload"), verifyErr: errors.New("digest mismatch")},
			destination: &replicaStore{}, descriptor: descriptor, want: "unverified"},
		{name: "truncated source", ctx: context.Background(), source: source, destination: &replicaStore{},
			descriptor: Descriptor{Digest: descriptor.Digest, Size: descriptor.Size + 1}, want: "size mismatch"},
		{name: "lying destination", ctx: context.Background(), source: source,
			destination: &replicaStore{putDescriptor: &Descriptor{Digest: "sha256:" + strings.Repeat("b", 64), Size: descriptor.Size}},
			descriptor:  descriptor, want: "descriptor mismatch"},
		{name: "corrupt existing destination", ctx: context.Background(), source: source,
			destination: &replicaStore{data: []byte("short"), exists: true},
			descriptor:  descriptor, want: "size mismatch"},
		{name: "canceled", ctx: canceledContext(), source: source, destination: &replicaStore{},
			descriptor: descriptor, want: "canceled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Replicate(test.ctx, test.source, test.destination, test.descriptor)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
		})
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

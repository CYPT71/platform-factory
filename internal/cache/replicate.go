package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Replicate copies one already-addressed blob between content stores without
// trusting either endpoint's metadata. The source is verified before export,
// the stream is bounded by the descriptor size, and the destination's
// returned descriptor and installed bytes are verified before success.
func Replicate(ctx context.Context, source, destination ContentStore, descriptor Descriptor) error {
	if source == nil || destination == nil {
		return errors.New("cache: source and destination stores are required")
	}
	if descriptor.Size < 0 {
		return errors.New("cache: replicated descriptor size must not be negative")
	}
	if _, err := parseDigest(descriptor.Digest); err != nil {
		return fmt.Errorf("cache: invalid replicated descriptor: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if exists, err := destination.Exists(descriptor.Digest); err != nil {
		return fmt.Errorf("cache: inspect replica destination: %w", err)
	} else if exists {
		if err := verifyStoredDescriptor(destination, descriptor); err != nil {
			return fmt.Errorf("cache: reject corrupt existing replica: %w", err)
		}
		return nil
	}
	if err := source.Verify(descriptor.Digest); err != nil {
		return fmt.Errorf("cache: reject unverified replica source: %w", err)
	}
	reader, err := source.Get(descriptor.Digest)
	if err != nil {
		return fmt.Errorf("cache: open replica source: %w", err)
	}
	defer reader.Close()

	counted := &countingReader{reader: io.LimitReader(reader, descriptor.Size+1)}
	installed, err := destination.Put(&contextReader{ctx: ctx, reader: counted})
	if err != nil {
		return fmt.Errorf("cache: install replica: %w", err)
	}
	if counted.read != descriptor.Size {
		return fmt.Errorf("cache: replica size mismatch: read %d, expected %d", counted.read, descriptor.Size)
	}
	if installed != descriptor {
		return fmt.Errorf("cache: replica descriptor mismatch: installed %+v, expected %+v", installed, descriptor)
	}
	if err := destination.Verify(descriptor.Digest); err != nil {
		return fmt.Errorf("cache: verify installed replica: %w", err)
	}
	return nil
}

func verifyStoredDescriptor(store ContentStore, descriptor Descriptor) error {
	if err := store.Verify(descriptor.Digest); err != nil {
		return err
	}
	reader, err := store.Get(descriptor.Digest)
	if err != nil {
		return err
	}
	defer reader.Close()
	n, err := io.Copy(io.Discard, io.LimitReader(reader, descriptor.Size+1))
	if err != nil {
		return err
	}
	if n != descriptor.Size {
		return fmt.Errorf("descriptor size mismatch: stored %d, expected %d", n, descriptor.Size)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type countingReader struct {
	reader io.Reader
	read   int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	r.read += int64(n)
	return n, err
}

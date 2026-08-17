package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const DefaultRemoteBlobLimit int64 = 1 << 30

// BlobHandler exposes a content store without adding naming or registry
// semantics: the URL digest remains the sole identity.
func BlobHandler(store ContentStore, limit int64) http.Handler {
	if limit <= 0 {
		limit = DefaultRemoteBlobLimit
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			temporary, err := os.CreateTemp("", "platform-factory-cas-upload-*")
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
	})
}

// PullBlob streams and verifies one remote blob into destination.
func PullBlob(ctx context.Context, client *http.Client, baseURL string, destination ContentStore, descriptor Descriptor, limit int64) error {
	if client == nil || destination == nil {
		return errors.New("cache: HTTP client and destination are required")
	}
	if limit <= 0 {
		limit = DefaultRemoteBlobLimit
	}
	if descriptor.Size < 0 || descriptor.Size > limit {
		return errors.New("cache: remote blob size exceeds limit")
	}
	if _, err := parseDigest(descriptor.Digest); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/"+descriptor.Digest, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("cache: remote blob returned HTTP %d", response.StatusCode)
	}
	counted := &countingReader{reader: io.LimitReader(response.Body, descriptor.Size+1)}
	installed, err := destination.Put(&contextReader{ctx: ctx, reader: counted})
	if err != nil {
		return err
	}
	if counted.read != descriptor.Size || installed != descriptor {
		return errors.New("cache: remote blob digest or size mismatch")
	}
	return destination.Verify(descriptor.Digest)
}

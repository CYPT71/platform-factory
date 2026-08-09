package layout

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxArchiveFiles = 10000
const maxArchiveBytes int64 = 32 << 20
const maxArchiveStreamBytes int64 = 40 << 20

var makeArchiveTempDir = os.MkdirTemp

// VerifyArchive extracts an OCI layout archive into a fresh private directory,
// rejects filesystem tricks and resource abuse, then applies the canonical
// layout verifier. No extracted pathname is ever supplied by the plugin.
func VerifyArchive(ctx context.Context, format string, source io.Reader) error {
	if format != "oci-layout.tar.gz" {
		return fmt.Errorf("layout archive: unsupported format %q", format)
	}
	gz, err := gzip.NewReader(&archiveContextReader{ctx: ctx, reader: source})
	if err != nil {
		return errors.New("layout archive: invalid gzip")
	}
	defer gz.Close()
	root, err := makeArchiveTempDir("", "platform-factory-layout-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	bounded := &archiveBoundedReader{reader: gz, remaining: maxArchiveStreamBytes + 1}
	tr := tar.NewReader(bounded)
	seen := map[string]struct{}{}
	var files int
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("layout archive: invalid tar")
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || strings.HasPrefix(name, "/") || path.Clean(name) != name || strings.ContainsRune(name, 0) {
			return fmt.Errorf("layout archive: unsafe path %q", h.Name)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("layout archive: duplicate path %q", name)
		}
		seen[name] = struct{}{}
		files++
		if files > maxArchiveFiles {
			return errors.New("layout archive: file count limit exceeded")
		}
		if h.Size < 0 || h.Size > maxArchiveBytes-total {
			return errors.New("layout archive: size limit exceeded")
		}
		total += h.Size
		target := filepath.Join(root, filepath.FromSlash(name))
		if !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return errors.New("layout archive: path escapes root")
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(f, io.LimitReader(tr, h.Size+1))
			closeErr := f.Close()
			if copyErr != nil || closeErr != nil || n != h.Size {
				return errors.Join(copyErr, closeErr, errors.New("layout archive: truncated entry"))
			}
			payload, readErr := os.ReadFile(target)
			if readErr != nil {
				return readErr
			}
			if containsSecretMarker(payload) {
				return errors.New("layout archive: secret-bearing content is forbidden")
			}
		default:
			return fmt.Errorf("layout archive: unsafe entry type for %q", name)
		}
	}
	if bounded.remaining <= 0 {
		return errors.New("layout archive: header and content limit exceeded")
	}
	// Force the gzip checksum/trailer to be consumed and reject concatenated
	// members or data hidden after the tar end markers.
	extra, trailerErr := io.Copy(io.Discard, io.LimitReader(gz, 1))
	if trailerErr != nil || extra != 0 {
		return errors.New("layout archive: trailing or corrupt compressed data")
	}
	if err := gz.Close(); err != nil {
		return errors.New("layout archive: invalid gzip trailer")
	}
	_, err = Verify(root)
	return err
}
func containsSecretMarker(data []byte) bool {
	lower := strings.ToLower(string(data))
	for _, m := range []string{"secret-sentinel", "password=", "secret=", "access_token=", "api_key=", "private_key=", "-----begin private key"} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

type ArchiveVerifier struct{}

func (ArchiveVerifier) VerifyArtifact(ctx context.Context, format string, r io.Reader) error {
	return VerifyArchive(ctx, format, r)
}

type archiveContextReader struct {
	ctx    context.Context
	reader io.Reader
}
type archiveBoundedReader struct {
	reader    io.Reader
	remaining int64
}

func (r *archiveBoundedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("layout archive: decompressed stream limit exceeded")
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (r *archiveContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

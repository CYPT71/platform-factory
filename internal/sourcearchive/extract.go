package sourcearchive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxFiles       = 10000
	maxPayload     = int64(256 << 20)
	maxStreamBytes = int64(320 << 20)
)

// Extract creates destination exclusively and extracts a plain or gzip tar
// source into it. On every error destination is removed completely.
func Extract(source, destination, format string) (err error) {
	if format != "tar" && format != "tar.gz" {
		return fmt.Errorf("source archive: unsupported format %q", format)
	}
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("source archive: open: %w", err)
	}
	defer input.Close()
	if err := os.Mkdir(destination, 0o700); err != nil {
		return fmt.Errorf("source archive: reserve destination: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(destination)
		}
	}()
	var reader io.Reader = input
	var gz *gzip.Reader
	if format == "tar.gz" {
		gz, err = gzip.NewReader(input)
		if err != nil {
			return errors.New("source archive: invalid gzip")
		}
		gz.Multistream(false)
		defer gz.Close()
		reader = gz
	}
	bounded := &boundedReader{reader: reader, remaining: maxStreamBytes + 1}
	tr := tar.NewReader(bounded)
	seen := map[string]bool{}
	files, total := 0, int64(0)
	for {
		header, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return errors.New("source archive: invalid or truncated tar")
		}
		name := strings.TrimSuffix(header.Name, "/")
		clean := path.Clean(name)
		if name == "" || strings.HasPrefix(name, "/") || clean != name || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(name, 0) {
			return fmt.Errorf("source archive: unsafe path %q", header.Name)
		}
		if seen[name] {
			return fmt.Errorf("source archive: duplicate path %q", name)
		}
		seen[name] = true
		files++
		if files > maxFiles {
			return errors.New("source archive: file count limit exceeded")
		}
		if header.Size < 0 || header.Size > maxPayload-total {
			return errors.New("source archive: payload limit exceeded")
		}
		total += header.Size
		target := filepath.Join(destination, filepath.FromSlash(name))
		if !strings.HasPrefix(target, destination+string(filepath.Separator)) {
			return errors.New("source archive: path escapes destination")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode) & 0o777
			mode &^= 0o022
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			written, copyErr := io.Copy(file, io.LimitReader(tr, header.Size+1))
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil || written != header.Size {
				return errors.Join(copyErr, closeErr, errors.New("source archive: truncated entry"))
			}
		default:
			return fmt.Errorf("source archive: links and special entry types are forbidden (%q)", name)
		}
	}
	if bounded.remaining <= 0 {
		return errors.New("source archive: decompressed stream limit exceeded")
	}
	if gz != nil {
		if extra, readErr := io.Copy(io.Discard, io.LimitReader(gz, 1)); readErr != nil || extra != 0 {
			return errors.New("source archive: trailing compressed data")
		}
		if err := gz.Close(); err != nil {
			return errors.New("source archive: invalid gzip trailer")
		}
		var trailing [1]byte
		if n, _ := input.Read(trailing[:]); n != 0 {
			return errors.New("source archive: concatenated or trailing gzip data")
		}
	}
	keep = true
	return nil
}

type boundedReader struct {
	reader    io.Reader
	remaining int64
}

func (r *boundedReader) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("source archive: stream limit exceeded")
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	n, err := r.reader.Read(buffer)
	r.remaining -= int64(n)
	return n, err
}

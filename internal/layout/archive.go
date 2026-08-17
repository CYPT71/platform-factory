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
func VerifyArchive(ctx context.Context, format string, source io.Reader) (Report, error) {
	if format != "oci-layout.tar.gz" {
		return Report{}, fmt.Errorf("layout archive: unsupported format %q", format)
	}
	gz, err := gzip.NewReader(&archiveContextReader{ctx: ctx, reader: source})
	if err != nil {
		return Report{}, errors.New("layout archive: invalid gzip")
	}
	defer gz.Close()
	root, err := makeArchiveTempDir("", "platform-factory-layout-*")
	if err != nil {
		return Report{}, err
	}
	defer os.RemoveAll(root)
	bounded := &archiveBoundedReader{reader: gz, remaining: maxArchiveStreamBytes + 1}
	tr := tar.NewReader(bounded)
	seen := map[string]struct{}{}
	var files int
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Report{}, errors.New("layout archive: invalid tar")
		}
		name := strings.TrimSuffix(h.Name, "/")
		clean := path.Clean(name)
		if name == "" || strings.HasPrefix(name, "/") || clean != name || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(name, 0) {
			return Report{}, fmt.Errorf("layout archive: unsafe path %q", h.Name)
		}
		if _, ok := seen[name]; ok {
			return Report{}, fmt.Errorf("layout archive: duplicate path %q", name)
		}
		seen[name] = struct{}{}
		files++
		if files > maxArchiveFiles {
			return Report{}, errors.New("layout archive: file count limit exceeded")
		}
		if h.Size < 0 || h.Size > maxArchiveBytes-total {
			return Report{}, errors.New("layout archive: size limit exceeded")
		}
		total += h.Size
		target := filepath.Join(root, filepath.FromSlash(name))
		if !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return Report{}, errors.New("layout archive: path escapes root")
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return Report{}, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return Report{}, err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return Report{}, err
			}
			n, copyErr := io.Copy(f, io.LimitReader(tr, h.Size+1))
			closeErr := f.Close()
			if copyErr != nil || closeErr != nil || n != h.Size {
				return Report{}, errors.Join(copyErr, closeErr, errors.New("layout archive: truncated entry"))
			}
			payload, readErr := os.ReadFile(target)
			if readErr != nil {
				return Report{}, readErr
			}
			if containsSecretMarker(payload) {
				return Report{}, errors.New("layout archive: secret-bearing content is forbidden")
			}
		default:
			return Report{}, fmt.Errorf("layout archive: unsafe entry type for %q", name)
		}
	}
	if bounded.remaining <= 0 {
		return Report{}, errors.New("layout archive: header and content limit exceeded")
	}
	// Force the gzip checksum/trailer to be consumed and reject concatenated
	// members or data hidden after the tar end markers.
	extra, trailerErr := io.Copy(io.Discard, io.LimitReader(gz, 1))
	if trailerErr != nil || extra != 0 {
		return Report{}, errors.New("layout archive: trailing or corrupt compressed data")
	}
	if err := gz.Close(); err != nil {
		return Report{}, errors.New("layout archive: invalid gzip trailer")
	}
	return Verify(root)
}

// unconditionalSecretMarkers never appear as an innocent source-code
// pattern - a literal PEM header or the project's own deliberate test
// sentinel - so any occurrence anywhere is reported without further
// inspection, exactly as before.
var unconditionalSecretMarkers = []string{"secret-sentinel", "-----begin private key"}

// assignmentSecretMarkers are "key=" shaped: real in a leaked .env/shell/
// config file, but also indistinguishable at the substring level from an
// ordinary keyword argument in real source code - confirmed by hand
// against Python's own standard library, where password=None,
// password=password (a pass-through), and similar shapes are completely
// idiomatic across every stdlib module offering password authentication
// (ftplib, nntplib, imaplib, smtplib, poplib). Bundling a real language
// runtime's standard library - the actual, intended use of
// PullImage/rootfs extraction elsewhere in this codebase - would
// otherwise never pass this check at all. Each occurrence is inspected
// for one of a small number of unambiguously non-secret shapes before
// being reported.
var assignmentSecretMarkers = []string{"password=", "secret=", "access_token=", "api_key=", "private_key="}

// safeAssignmentValues are the value-shapes confirmed, by inspecting
// every real match Python's own standard library produced, to never be
// an actual secret: a Python/Go/JS falsy or absent default, or a member
// access naming another already-authenticated object rather than
// embedding a literal value.
var safeAssignmentValues = []string{"none", "true", "false", "null", "self"}

func containsSecretMarker(data []byte) bool {
	lower := strings.ToLower(string(data))
	for _, m := range unconditionalSecretMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	for _, m := range assignmentSecretMarkers {
		if containsSuspiciousAssignment(lower, m) {
			return true
		}
	}
	return false
}

// containsSuspiciousAssignment scans every occurrence of marker (a
// "key="-shaped string) in lower and reports whether any of them carries
// a value that isn't one of the confirmed-benign shapes: a falsy/absent
// default (safeAssignmentValues), an empty quoted string (an explicit,
// contentless default), or a self-referencing keyword argument
// (key=key, e.g. Python's own password=password pass-through pattern -
// detected by comparing against the identifier immediately preceding the
// marker itself).
func containsSuspiciousAssignment(lower, marker string) bool {
	searchFrom := 0
	for {
		index := strings.Index(lower[searchFrom:], marker)
		if index < 0 {
			return false
		}
		start := searchFrom + index
		after := lower[start+len(marker):]
		searchFrom = start + len(marker)

		if isSafeAssignmentValue(after) {
			continue
		}
		if isEmptyQuotedValue(after) {
			continue
		}
		if isSelfReferencingKeyword(marker, after) {
			continue
		}
		return true
	}
}

func isSafeAssignmentValue(after string) bool {
	for _, safe := range safeAssignmentValues {
		if !strings.HasPrefix(after, safe) {
			continue
		}
		rest := after[len(safe):]
		if rest == "" || !isIdentifierByte(rest[0]) {
			return true
		}
	}
	return false
}

func isEmptyQuotedValue(after string) bool {
	if after == "" {
		return false
	}
	quote := after[0]
	return (quote == '"' || quote == '\'') && len(after) > 1 && after[1] == quote
}

// isSelfReferencingKeyword reports whether marker's value is the exact
// same identifier as its own keyword name - Python's password=password
// (and equivalents) pass an already-held value through rather than
// embedding a literal one. A leading "{" (and, for a Go/JS-style "${",
// two characters) is skipped first: an f-string/template-literal
// interpolation like password={password!r} or password=${password} is
// the same pass-through pattern, just spelled with interpolation syntax.
func isSelfReferencingKeyword(marker, after string) bool {
	name := strings.TrimSuffix(marker, "=")
	if idx := strings.LastIndexAny(name, ". \t\n(,"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return false
	}
	value := strings.TrimPrefix(strings.TrimPrefix(after, "$"), "{")
	if !strings.HasPrefix(value, name) {
		return false
	}
	rest := value[len(name):]
	return rest == "" || !isIdentifierByte(rest[0])
}

func isIdentifierByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

type ArchiveVerifier struct{}

func (ArchiveVerifier) VerifyArtifact(ctx context.Context, format string, r io.Reader) error {
	_, err := VerifyArchive(ctx, format, r)
	return err
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

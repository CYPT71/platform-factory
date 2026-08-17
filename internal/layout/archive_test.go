package layout

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archiveBytes(t *testing.T, entries map[string]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, kind := range entries {
		h := &tar.Header{Name: name, Mode: 0o600, Typeflag: kind}
		if kind == tar.TypeReg {
			h.Size = 1
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			_, _ = tw.Write([]byte("x"))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return b.Bytes()
}
func TestVerifyArchiveRejectsHostileEntries(t *testing.T) {
	for name, entries := range map[string]map[string]byte{"traversal": {"../x": tar.TypeReg}, "absolute": {"/x": tar.TypeReg}, "symlink": {"x": tar.TypeSymlink}, "hardlink": {"x": tar.TypeLink}, "duplicate": nil} {
		t.Run(name, func(t *testing.T) {
			var data []byte
			if name == "duplicate" {
				var b bytes.Buffer
				gz := gzip.NewWriter(&b)
				tw := tar.NewWriter(gz)
				for range 2 {
					_ = tw.WriteHeader(&tar.Header{Name: "x", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
					_, _ = tw.Write([]byte("x"))
				}
				_ = tw.Close()
				_ = gz.Close()
				data = b.Bytes()
			} else {
				data = archiveBytes(t, entries)
			}
			if _, err := VerifyArchive(context.Background(), "oci-layout.tar.gz", bytes.NewReader(data)); err == nil {
				t.Fatal("hostile archive accepted")
			}
		})
	}
}
func TestVerifyArchiveRejectsConcatenatedAndTrailingGzip(t *testing.T) {
	base := archiveBytes(t, map[string]byte{"x": tar.TypeReg})
	for name, data := range map[string][]byte{"concatenated": append(append([]byte(nil), base...), base...), "trailing": append(append([]byte(nil), base...), []byte("junk")...)} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyArchive(context.Background(), "oci-layout.tar.gz", bytes.NewReader(data)); err == nil {
				t.Fatal("trailing stream accepted")
			}
		})
	}
}
func TestVerifyArchiveCleansTemporaryDirectoryOnFailure(t *testing.T) {
	original := makeArchiveTempDir
	var created string
	makeArchiveTempDir = func(dir, pattern string) (string, error) {
		root, err := os.MkdirTemp(dir, pattern)
		created = root
		return root, err
	}
	t.Cleanup(func() { makeArchiveTempDir = original })
	_, _ = VerifyArchive(context.Background(), "oci-layout.tar.gz", bytes.NewReader(archiveBytes(t, map[string]byte{"../escape": tar.TypeReg})))
	if created == "" {
		t.Fatal("temporary directory was not created")
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("temporary directory remains: %v", err)
	}
}
func TestVerifyArchiveRejectsOversizedPAXMetadata(t *testing.T) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	err := tw.WriteHeader(&tar.Header{Name: "x", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg, PAXRecords: map[string]string{"comment": strings.Repeat("x", int(maxArchiveStreamBytes+1))}})
	if err == nil {
		_, _ = tw.Write([]byte("x"))
	}
	_ = tw.Close()
	_ = gz.Close()
	if _, err := VerifyArchive(context.Background(), "oci-layout.tar.gz", bytes.NewReader(b.Bytes())); err == nil {
		t.Fatal("oversized PAX metadata accepted")
	}
}
func TestVerifyArchiveAcceptsVerifiedLayout(t *testing.T) {
	root := buildLayout(t)
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	err := filepath.Walk(root, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if name == root {
			return nil
		}
		rel, _ := filepath.Rel(root, name)
		h, _ := tar.FileInfoHeader(info, "")
		h.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			data, _ := os.ReadFile(name)
			_, err = tw.Write(data)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	if _, err := VerifyArchive(context.Background(), "oci-layout.tar.gz", bytes.NewReader(b.Bytes())); err != nil {
		t.Fatal(err)
	}
}

// TestContainsSecretMarkerAcceptsRealStandardLibraryPatterns pins down a
// real false-positive found by hand: bundling CPython's own standard
// library (via internal/rootfs's pulled-image extraction, used by
// `pf plugin provision-runtime`) tripped this check on ordinary keyword
// arguments across ftplib/nntplib/imaplib - none of them a leaked secret.
// Each line here is copied verbatim from the real match.
func TestContainsSecretMarkerAcceptsRealStandardLibraryPatterns(t *testing.T) {
	benign := []string{
		"def __init__(self, host, port=NNTP_PORT, user=None, password=None,",
		"    def login(self, user=None, password=None, usenetrc=True):",
		"                    password=password,",
		"    def upload_file(self, metadata, filename, signer=None, sign_password=None,",
		"        key_password=None,",
		"            key_password=self.key_password,",
		"                password=True,",
		`        print(f"password={password!r}")`,
		"        return console.input(prompt, password=password, stream=stream)",
	}
	for _, line := range benign {
		if containsSecretMarker([]byte(line)) {
			t.Fatalf("flagged benign source line as a secret: %q", line)
		}
	}
}

// TestContainsSecretMarkerStillRejectsRealSecrets is the regression half
// of the fix above: a genuinely leaked credential (an actual, non-falsy,
// non-self-referencing value) must still be caught.
func TestContainsSecretMarkerStillRejectsRealSecrets(t *testing.T) {
	leaks := []string{
		"secret-sentinel",
		// Deliberately not a real (or even structurally plausible) PEM/DER
		// prefix - containsSecretMarker only ever checks for the header
		// string itself, never anything about what follows it, so a
		// placeholder that a secret scanner cannot mistake for genuine key
		// material tests the same code path just as well.
		"-----BEGIN PRIVATE KEY-----\nNOT-REAL-KEY-MATERIAL-TEST-FIXTURE-ONLY",
		`password="hunter2"`,
		"password=hunter2",
		"SECRET=sk_live_abcdef1234567890",
		"ACCESS_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"API_KEY=AKIAIOSFODNN7EXAMPLE",
		"private_key=not-a-real-secret-test-fixture-value",
	}
	for _, line := range leaks {
		if !containsSecretMarker([]byte(line)) {
			t.Fatalf("failed to flag a real-looking secret: %q", line)
		}
	}
}

// TestContainsSecretMarkerAcceptsFalsyAndEmptyDefaults covers the
// specific safe shapes the fix recognizes, independent of any one
// language's real source - Python/Go/JS-style falsy/absent defaults and
// an explicitly empty quoted default.
func TestContainsSecretMarkerAcceptsFalsyAndEmptyDefaults(t *testing.T) {
	safe := []string{
		"secret=None", "secret=none", "secret=null", "secret=true", "secret=false",
		`secret=""`, "secret=''",
		"api_key=self.api_key",
	}
	for _, line := range safe {
		if containsSecretMarker([]byte(line)) {
			t.Fatalf("flagged a falsy/empty/self-referencing default as a secret: %q", line)
		}
	}
}

// TestContainsSecretMarkerDoesNotMatchOnPrefixWords guards against the
// safe-value check itself becoming a new false-negative: a value that
// merely *starts with* a safe word (e.g. "nonely" starting with "none")
// must not be waved through.
func TestContainsSecretMarkerDoesNotMatchOnPrefixWords(t *testing.T) {
	if !containsSecretMarker([]byte("secret=nonely-a-real-value")) {
		t.Fatal("a value merely prefixed by a safe word must still be flagged")
	}
	if !containsSecretMarker([]byte("secret=selfsame")) {
		t.Fatal("a value merely prefixed by 'self' must still be flagged")
	}
}

package rootfs

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// failingWriter accepts exactly afterBytes bytes and fails every write
// after that, so tests can force a write failure at a precise offset -
// something gzip's own internal buffering would otherwise mask (see
// WriteInitramfs's doc comment on why this package doesn't try to inject
// failures through the full gzip pipeline).
type failingWriter struct {
	afterBytes int
	written    int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.written >= w.afterBytes {
		return 0, errors.New("write failed")
	}
	n := len(p)
	if w.written+n > w.afterBytes {
		n = w.afterBytes - w.written
	}
	w.written += n
	if n < len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func TestWriteHeaderPropagatesUnderlyingWriteFailures(t *testing.T) {
	newWriter := func(afterBytes int) *cpioWriter {
		return &cpioWriter{w: bufio.NewWriterSize(&failingWriter{afterBytes: afterBytes}, 1)}
	}
	if err := newWriter(0).writeHeader("x", 0, 0, 1); err == nil {
		t.Fatal("header bytes write failure not propagated")
	}
	// The newc header is exactly 110 bytes; letting that much through
	// forces the failure onto the following name write instead.
	if err := newWriter(110).writeHeader("x", 0, 0, 1); err == nil {
		t.Fatal("name bytes write failure not propagated")
	}
}

func TestWriteEntryPropagatesLstatFailure(t *testing.T) {
	c := &cpioWriter{w: bufio.NewWriter(io.Discard)}
	if err := c.writeEntry(t.TempDir(), "missing", 1); err == nil {
		t.Fatal("lstat failure not propagated")
	}
}

func TestWriteEntryPropagatesRegularFileCopyFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// header(110) + name "f\0"(2) = 112, already 4-byte aligned, so the
	// failure lands squarely on the file-content copy.
	c := &cpioWriter{w: bufio.NewWriterSize(&failingWriter{afterBytes: 112}, 1)}
	if err := c.writeEntry(root, "f", 1); err == nil {
		t.Fatal("file content write failure not propagated")
	}
}

func TestWriteEntryPropagatesSymlinkFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks behave differently on Windows")
	}
	root := t.TempDir()
	if err := os.Symlink("target", filepath.Join(root, "s")); err != nil {
		t.Fatal(err)
	}
	if err := (&cpioWriter{w: bufio.NewWriterSize(&failingWriter{afterBytes: 0}, 1)}).writeEntry(root, "s", 1); err == nil {
		t.Fatal("symlink header failure not propagated")
	}
	// header(110) + name "s\0"(2) = 112, so the header succeeds and the
	// failure lands on the symlink target write.
	if err := (&cpioWriter{w: bufio.NewWriterSize(&failingWriter{afterBytes: 112}, 1)}).writeEntry(root, "s", 1); err == nil {
		t.Fatal("symlink target write failure not propagated")
	}
}

// cpioEntry is a minimal decode of one newc entry, used only to verify
// WriteInitramfs's own output in tests.
type cpioEntry struct {
	name string
	mode uint32
	data []byte
}

func decodeNewc(t *testing.T, archive []byte) []cpioEntry {
	t.Helper()
	var entries []cpioEntry
	offset := 0
	for {
		if offset+110 > len(archive) {
			t.Fatalf("truncated header at offset %d", offset)
		}
		header := archive[offset : offset+110]
		if string(header[:6]) != "070701" {
			t.Fatalf("bad magic %q at offset %d", header[:6], offset)
		}
		field := func(index int) uint32 {
			value, err := strconv.ParseUint(string(header[6+index*8:6+index*8+8]), 16, 32)
			if err != nil {
				t.Fatalf("decode field %d: %v", index, err)
			}
			return uint32(value)
		}
		mode := field(1)
		filesize := field(6)
		namesize := field(11)
		offset += 110
		if offset+int(namesize) > len(archive) {
			t.Fatalf("truncated name at offset %d", offset)
		}
		name := string(archive[offset : offset+int(namesize)-1])
		offset += int(namesize)
		if remainder := offset % 4; remainder != 0 {
			offset += 4 - remainder
		}
		if offset+int(filesize) > len(archive) {
			t.Fatalf("truncated data at offset %d", offset)
		}
		data := archive[offset : offset+int(filesize)]
		offset += int(filesize)
		if remainder := offset % 4; remainder != 0 {
			offset += 4 - remainder
		}
		if name == "TRAILER!!!" {
			break
		}
		entries = append(entries, cpioEntry{name: name, mode: mode, data: append([]byte(nil), data...)})
	}
	if offset != len(archive) {
		t.Fatalf("trailing bytes after TRAILER!!!: %d remaining", len(archive)-offset)
	}
	return entries
}

func gunzip(t *testing.T, compressed []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return data
}

func convertedRootfs(t *testing.T) string {
	t.Helper()
	layout := writeTestLayout(t, []testEntry{
		{name: "bin/", mode: 0o755, kind: tar.TypeDir},
		{name: "bin/app", body: "payload", mode: 0o755, kind: tar.TypeReg},
		{name: "bin/current", link: "app", mode: 0o777, kind: tar.TypeSymlink},
	})
	output := filepath.Join(t.TempDir(), "rootfs")
	if _, err := Convert(Options{Layout: layout, Output: output}); err != nil {
		t.Fatal(err)
	}
	return output
}

func writeStubInitBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "microvm-init")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /app/service\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWriteInitramfsProducesDecodableArchive(t *testing.T) {
	dir := convertedRootfs(t)
	initBinary := writeStubInitBinary(t)
	if err := InstallInit(dir, initBinary, []string{"/app/service", "-flag"}); err != nil {
		t.Fatal(err)
	}

	var buffer bytes.Buffer
	if err := WriteInitramfs(dir, &buffer); err != nil {
		t.Fatal(err)
	}
	entries := decodeNewc(t, gunzip(t, buffer.Bytes()))

	byName := map[string]cpioEntry{}
	var names []string
	for _, entry := range entries {
		byName[entry.name] = entry
		names = append(names, entry.name)
	}
	for _, want := range []string{"bin", "bin/app", "bin/current", "sbin", "sbin/init", "etc/platform-factory", "etc/platform-factory/entrypoint.json"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("missing entry %q among %v", want, names)
		}
	}
	if got := byName["bin/app"].data; string(got) != "payload" {
		t.Fatalf("bin/app data = %q", got)
	}
	if got := byName["bin/current"].data; string(got) != "app" {
		t.Fatalf("bin/current symlink target = %q", got)
	}
	if byName["bin/current"].mode&cpioModeSymlink == 0 {
		t.Fatalf("bin/current mode %o missing symlink bit", byName["bin/current"].mode)
	}
	if byName["bin"].mode&cpioModeDir == 0 {
		t.Fatalf("bin mode %o missing dir bit", byName["bin"].mode)
	}
	if byName["sbin/init"].mode&cpioModeReg == 0 {
		t.Fatalf("sbin/init mode %o missing regular-file bit", byName["sbin/init"].mode)
	}
	if got := string(byName["etc/platform-factory/entrypoint.json"].data); got != `["/app/service","-flag"]` {
		t.Fatalf("entrypoint.json = %q", got)
	}

	sortedNames := append([]string(nil), names...)
	for i := 1; i < len(sortedNames); i++ {
		if sortedNames[i-1] >= sortedNames[i] {
			t.Fatalf("entries not in sorted order: %q before %q", sortedNames[i-1], sortedNames[i])
		}
	}
}

func TestWriteInitramfsIsDeterministic(t *testing.T) {
	dir := convertedRootfs(t)
	if err := InstallInit(dir, writeStubInitBinary(t), nil); err != nil {
		t.Fatal(err)
	}

	var first, second bytes.Buffer
	if err := WriteInitramfs(dir, &first); err != nil {
		t.Fatal(err)
	}
	if err := WriteInitramfs(dir, &second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("initramfs is not byte-reproducible: %s vs %s",
			hex.EncodeToString(first.Bytes()[:16]), hex.EncodeToString(second.Bytes()[:16]))
	}
}

func TestWriteInitramfsRejectsMissingSource(t *testing.T) {
	if err := WriteInitramfs(filepath.Join(t.TempDir(), "missing"), io.Discard); err == nil {
		t.Fatal("missing source directory accepted")
	}
}

func TestInstallInitWritesExecutableInit(t *testing.T) {
	dir := convertedRootfs(t)
	if err := InstallInit(dir, writeStubInitBinary(t), nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "sbin", "init"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o555 {
		t.Fatalf("init mode = %o, want 0555", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, "etc", "platform-factory", "entrypoint.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("entrypoint.json written without an entrypoint: err=%v", err)
	}
}

func TestInstallProcessConfigWritesValidatedOCIContract(t *testing.T) {
	dir := t.TempDir()
	umask := uint32(0o027)
	config := ProcessConfig{
		Args: []string{"/app/service", "--serve"}, Env: []string{"A=B"}, Cwd: "/work",
		UID: 1000, GID: 1001, Groups: []uint32{1002}, Umask: &umask,
		Rlimits: []ProcessRlimit{{Type: "RLIMIT_NOFILE", Soft: 1024, Hard: 2048}},
	}
	if err := InstallProcessConfig(dir, config); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "etc", "platform-factory", "process.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"/app/service"`, `"A=B"`, `"/work"`, `"uid":1000`, `"gid":1001`, `1002`, `"umask":23`, `"RLIMIT_NOFILE"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("process config missing %q: %s", want, data)
		}
	}
	for _, invalid := range []ProcessConfig{
		{}, {Args: []string{"relative"}}, {Args: []string{"/app"}, Cwd: "relative"},
		{Args: []string{"/app"}, Env: []string{"INVALID"}},
		{Args: []string{"/app\x00bad"}},
	} {
		if err := InstallProcessConfig(dir, invalid); err == nil {
			t.Fatalf("invalid config accepted: %+v", invalid)
		}
	}
}

func TestInstallGuestTransportConfigKeepsSessionKeyPrivate(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{0x42}, 32)
	if err := InstallGuestTransportConfig(dir, key); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "etc", "platform-factory", "guest-session.key")
	encoded, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(encoded)) != strings.Repeat("42", 32) {
		t.Fatalf("encoded session key=%q", encoded)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session key mode=%v err=%v", info.Mode().Perm(), err)
	}
	metadata, err := os.ReadFile(filepath.Join(dir, "etc", "platform-factory", "guest-transport.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metadata), `"/dev/ttyS1"`) ||
		!strings.Contains(string(metadata), GuestSessionKeyPath) ||
		bytes.Contains(metadata, encoded) {
		t.Fatalf("guest transport metadata=%s", metadata)
	}
	for _, invalid := range [][]byte{nil, bytes.Repeat([]byte{1}, 31), bytes.Repeat([]byte{1}, 4097)} {
		if err := InstallGuestTransportConfig(dir, invalid); err == nil {
			t.Fatalf("invalid key length %d accepted", len(invalid))
		}
	}
}

func TestInstallInitRejectsMissingInitBinary(t *testing.T) {
	dir := convertedRootfs(t)
	if err := InstallInit(dir, filepath.Join(t.TempDir(), "missing-init"), nil); err == nil {
		t.Fatal("missing init binary accepted")
	}
}

func TestInstallInitRejectsMissingDirectory(t *testing.T) {
	if err := InstallInit(filepath.Join(t.TempDir(), "missing"), writeStubInitBinary(t), nil); err == nil {
		t.Fatal("missing target directory accepted")
	}
}

func TestInstallInitSurfacesMkdirAllFailures(t *testing.T) {
	t.Run("sbin collides with a file", func(t *testing.T) {
		dir := convertedRootfs(t)
		if err := os.WriteFile(filepath.Join(dir, "sbin"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := InstallInit(dir, writeStubInitBinary(t), nil); err == nil {
			t.Fatal("sbin colliding with a regular file accepted")
		}
	})
	t.Run("etc collides with a file", func(t *testing.T) {
		dir := convertedRootfs(t)
		if err := os.WriteFile(filepath.Join(dir, "etc"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := InstallInit(dir, writeStubInitBinary(t), []string{"/app/service"}); err == nil {
			t.Fatal("etc colliding with a regular file accepted")
		}
	})
}

func TestWriteInitramfsRejectsUnsupportedEntryType(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mkfifo has no Windows equivalent")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteInitramfs(dir, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unsupported entry type") {
		t.Fatalf("fifo entry err = %v, want unsupported entry type", err)
	}
}

func TestWriteInitramfsPropagatesWalkErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not restrict traversal the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "app"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blocked, 0o755)

	if err := WriteInitramfs(dir, io.Discard); err == nil {
		t.Fatal("unreadable subdirectory accepted")
	}
}

func TestInstallInitRejectsInvalidEntrypoint(t *testing.T) {
	tests := []struct {
		name       string
		entrypoint []string
	}{
		{"relative path", []string{"service"}},
		{"NUL byte", []string{"/app/service", "bad\x00arg"}},
		{"too many arguments", make([]string, 129)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if len(test.entrypoint) > 0 && test.entrypoint[0] == "" {
				test.entrypoint[0] = "/app/service"
			}
			dir := convertedRootfs(t)
			if err := InstallInit(dir, writeStubInitBinary(t), test.entrypoint); err == nil {
				t.Fatal("invalid entrypoint accepted")
			}
		})
	}
}

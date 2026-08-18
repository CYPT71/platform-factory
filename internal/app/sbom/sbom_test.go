package sbom

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectPathsWithRealFilesystem(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a.txt", "a")
	mustWrite("nested/b.txt", "b")

	svc := New()
	paths, err := svc.CollectPaths([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths=%v, want 2 entries", paths)
	}
}

func TestCollectPathsRejectsNonRegularNonDirArguments(t *testing.T) {
	svc := &service{stat: func(name string) (os.FileInfo, error) {
		return fakeFileInfo{mode: os.ModeSymlink}, nil
	}}
	if _, err := svc.CollectPaths([]string{"whatever"}); err == nil {
		t.Fatal("expected an error for a non-regular, non-directory argument")
	}
}

func TestCollectPathsPropagatesStatError(t *testing.T) {
	svc := New()
	if _, err := svc.CollectPaths([]string{filepath.Join(t.TempDir(), "does-not-exist")}); err == nil {
		t.Fatal("expected an error for a nonexistent path")
	}
}

func TestGenerateAndWriteJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New()
	paths, err := svc.CollectPaths([]string{file})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := svc.Generate(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Components) != 1 {
		t.Fatalf("components=%v, want 1", doc.Components)
	}
	var buf writerBuffer
	if err := svc.WriteJSON(&buf, doc); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty JSON output")
	}
}

type fakeFileInfo struct{ mode os.FileMode }

func (f fakeFileInfo) Name() string       { return "fake" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

// writerBuffer avoids importing bytes just for one small test.
type writerBuffer struct{ data []byte }

func (b *writerBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}
func (b *writerBuffer) Len() int { return len(b.data) }

package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteJSONCreatesDirAndWritesReadableMode(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "reports")
	if err := WriteJSON(dir, "build.json", map[string]string{"digest": "sha256:abc"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "build.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode=%o, want 0644", info.Mode().Perm())
	}
	if dirInfo, err := os.Stat(dir); err != nil || dirInfo.Mode().Perm() != 0o755 {
		t.Fatalf("dir mode=%v err=%v, want 0755", dirInfo, err)
	}
	var decoded map[string]string
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &decoded) != nil || decoded["digest"] != "sha256:abc" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestWriteJSONSensitiveCreatesDirAndWritesRestrictedMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "publication", "policy.json")
	if err := WriteJSONSensitive(path, map[string]bool{"require_signature": true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o, want 0600", info.Mode().Perm())
	}
	dir := filepath.Dir(path)
	if dirInfo, err := os.Stat(dir); err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode=%v err=%v, want 0700", dirInfo, err)
	}
}

func TestWriteJSONSensitiveRejectsUnmarshalableValues(t *testing.T) {
	if err := WriteJSONSensitive(filepath.Join(t.TempDir(), "x.json"), make(chan int)); err == nil {
		t.Fatal("expected an error for an unmarshalable value")
	}
}

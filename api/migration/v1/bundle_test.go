package v1

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBundleWritesReadsDigestsAndStableOrder(t *testing.T) {
	root := t.TempDir()
	documents := map[string][]byte{"resources/workloads.yaml": []byte("workloads: []\n"), "graph.yaml": []byte("edges: []\n"), "compatibility.yaml": []byte("gaps: []\n")}
	manifest, err := WriteBundle(root, documents)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{manifest.Documents[0].Path, manifest.Documents[1].Path, manifest.Documents[2].Path}; !reflect.DeepEqual(got, []string{"compatibility.yaml", "graph.yaml", "resources/workloads.yaml"}) {
		t.Fatalf("order=%v", got)
	}
	loaded, got, err := ReadBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest, loaded) || !reflect.DeepEqual(documents, got) {
		t.Fatalf("loaded=%+v documents=%v", loaded, got)
	}
	if err := os.WriteFile(filepath.Join(root, "graph.yaml"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBundle(root); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tamper err=%v", err)
	}
}

func TestBundleRejectsTraversalAbsoluteDuplicateAndSymlink(t *testing.T) {
	for _, name := range []string{"../escape.yaml", "/absolute.yaml", "a/../../escape", `a\\b.yaml`, "."} {
		if _, err := WriteBundle(t.TempDir(), map[string][]byte{name: []byte("x")}); err == nil {
			t.Fatalf("unsafe path accepted: %q", name)
		}
	}
	root := t.TempDir()
	if _, err := WriteBundle(root, map[string][]byte{"a.yaml": []byte("a"), "b.yaml": []byte("b")}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "environment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "b.yaml", "a.yaml", 1))
	if err := os.WriteFile(filepath.Join(root, "environment.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBundle(root); err == nil || !strings.Contains(err.Error(), "uniquely sorted") {
		t.Fatalf("duplicate err=%v", err)
	}
	linkRoot := t.TempDir()
	if _, err := WriteBundle(linkRoot, map[string][]byte{"doc.yaml": []byte("safe")}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(linkRoot, "doc.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a.yaml"), filepath.Join(linkRoot, "doc.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := ReadBundle(linkRoot); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink err=%v", err)
	}
}

func TestBundleStrictlyValidatesManifestAtRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "environment.yaml"), []byte("format_version: bad\ndocuments: []\nunknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBundle(root); err == nil {
		t.Fatal("invalid manifest accepted")
	}
}

func TestBundleRefusesSecretContentBeforeWrite(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteBundle(root, map[string][]byte{"resource.yaml": []byte("description: password=sentinel\n")}); err == nil {
		t.Fatal("secret bundle written")
	}
	if _, err := os.Stat(filepath.Join(root, "resource.yaml")); !os.IsNotExist(err) {
		t.Fatalf("secret document exists: %v", err)
	}
}

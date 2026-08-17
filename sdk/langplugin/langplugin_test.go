package langplugin

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDispatch(t *testing.T) {
	called := false
	handlers := map[string]Handler{"inspect": func(args []string) error {
		called = len(args) == 1 && args[0] == "root"
		return nil
	}}
	if err := Dispatch([]string{"inspect", "root"}, handlers); err != nil || !called {
		t.Fatalf("called=%t err=%v", called, err)
	}
	for _, args := range [][]string{nil, {"unknown"}} {
		if err := Dispatch(args, handlers); !errors.Is(err, ErrUsage) {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
}

func TestWriteDeterministicTarProducesSortedZeroedContent(t *testing.T) {
	source := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(source, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("zeta/z.txt", "z")
	mustWrite("alpha/a.txt", "a")

	output := filepath.Join(t.TempDir(), "out.tar")
	if err := WriteDeterministicTar(source, "app/deps", output); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	tr := tar.NewReader(file)
	var names []string
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !header.ModTime.Equal(time.Unix(0, 0)) && !header.ModTime.IsZero() {
			t.Fatalf("entry %q has non-zeroed ModTime %v", header.Name, header.ModTime)
		}
		names = append(names, header.Name)
	}
	want := []string{"app/deps/alpha/", "app/deps/alpha/a.txt", "app/deps/zeta/", "app/deps/zeta/z.txt"}
	if len(names) != len(want) {
		t.Fatalf("names=%v want=%v", names, want)
	}
	for i, name := range names {
		if name != want[i] {
			t.Fatalf("names=%v want=%v", names, want)
		}
	}
}

func TestWriteDeterministicTarRejectsSymlinks(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(source, "real")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	output := filepath.Join(t.TempDir(), "out.tar")
	if err := WriteDeterministicTar(source, "app/deps", output); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestWriteDeterministicTarIsDeterministic(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(t.TempDir(), "first.tar")
	second := filepath.Join(t.TempDir(), "second.tar")
	if err := WriteDeterministicTar(source, "app/deps", first); err != nil {
		t.Fatal(err)
	}
	if err := WriteDeterministicTar(source, "app/deps", second); err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("expected two runs over identical input to produce byte-identical output")
	}
}

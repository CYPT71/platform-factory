package langplugin

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRootFlag(t *testing.T) {
	t.Run("missing root is an error", func(t *testing.T) {
		if _, err := ParseRootFlag("inspect", nil); err == nil {
			t.Fatal("expected an error when --root is missing")
		}
	})
	t.Run("success", func(t *testing.T) {
		root, err := ParseRootFlag("inspect", []string{"--root", "/some/dir"})
		if err != nil {
			t.Fatal(err)
		}
		if root != "/some/dir" {
			t.Fatalf("root=%q, want /some/dir", root)
		}
	})
	t.Run("unknown flag is a parse error", func(t *testing.T) {
		if _, err := ParseRootFlag("inspect", []string{"--bogus"}); err == nil {
			t.Fatal("expected a flag parse error")
		}
	})
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !FileExists(file) {
		t.Fatal("expected the regular file to exist")
	}
	if FileExists(dir) {
		t.Fatal("a directory should not be reported as an existing file")
	}
	if FileExists(filepath.Join(dir, "missing")) {
		t.Fatal("a missing path should not be reported as an existing file")
	}
}

func TestRunIn(t *testing.T) {
	t.Run("success runs the command in the given directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := RunIn(dir, "touch", "marker.txt"); err != nil {
			t.Fatal(err)
		}
		if !FileExists(filepath.Join(dir, "marker.txt")) {
			t.Fatal("expected marker.txt to be created relative to the given directory")
		}
	})
	t.Run("failure surfaces an error", func(t *testing.T) {
		if err := RunIn(t.TempDir(), "platform-factory-definitely-not-a-real-binary"); err == nil {
			t.Fatal("expected an error for a nonexistent binary")
		}
	})
}

func TestParseBuildLayerFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"all present", []string{"--root", "r", "--output", "o", "--dest", "d"}, true},
		{"missing root", []string{"--output", "o", "--dest", "d"}, false},
		{"missing output", []string{"--root", "r", "--dest", "d"}, false},
		{"missing dest", []string{"--root", "r", "--output", "o"}, false},
		{"nothing", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, output, dest, err := ParseBuildLayerFlags(c.args)
			if c.want {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if root != "r" || output != "o" || dest != "d" {
					t.Fatalf("root=%q output=%q dest=%q", root, output, dest)
				}
			} else if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestBuildLayerSuccess(t *testing.T) {
	root := t.TempDir()
	depsDir := filepath.Join(root, ".platform-factory", "deps", "python")
	if err := os.MkdirAll(depsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depsDir, "pkg.py"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "layer.tar")

	args := []string{"--root", root, "--output", output, "--dest", "app/deps"}
	if err := BuildLayer(args, ".platform-factory/deps/python", "platform-factory-lang-python"); err != nil {
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
		names = append(names, header.Name)
	}
	if len(names) != 1 || names[0] != "app/deps/pkg.py" {
		t.Fatalf("names=%v, want [app/deps/pkg.py]", names)
	}
}

func TestBuildLayerMissingDependenciesDirectory(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(t.TempDir(), "layer.tar")
	args := []string{"--root", root, "--output", output, "--dest", "app/deps"}

	err := BuildLayer(args, ".platform-factory/deps/python", "platform-factory-lang-python")
	if err == nil {
		t.Fatal("expected an error when the dependencies directory is missing")
	}
	if got := err.Error(); !strings.Contains(got, "platform-factory-lang-python freeze") {
		t.Fatalf("err=%q, want it to name the freeze command", got)
	}
}

func TestBuildLayerDependenciesPathIsNotADirectory(t *testing.T) {
	root := t.TempDir()
	depsPath := filepath.Join(root, "deps")
	if err := os.WriteFile(depsPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "layer.tar")
	args := []string{"--root", root, "--output", output, "--dest", "app/deps"}

	err := BuildLayer(args, "deps", "platform-factory-lang-python")
	if err == nil {
		t.Fatal("expected an error when the dependencies path is not a directory")
	}
	if got := err.Error(); !strings.Contains(got, "is not a directory") {
		t.Fatalf("err=%q", got)
	}
}

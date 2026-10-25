package ciguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRecursiveLockDetectsDrift(t *testing.T) {
	for _, mutation := range []string{"content", "add", "delete", "executable", "directory", "symlink"} {
		t.Run(mutation, func(t *testing.T) {
			dir := t.TempDir()
			os.Mkdir(filepath.Join(dir, "src"), 0755)
			file := filepath.Join(dir, "src", "app")
			os.WriteFile(file, []byte("original"), 0644)
			lock, err := Snapshot(dir, []string{"src"})
			if err != nil {
				t.Fatal(err)
			}
			if err := Verify(dir, lock); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "content":
				os.WriteFile(file, []byte("modified"), 0644)
			case "add":
				os.WriteFile(filepath.Join(dir, "src", "extra"), nil, 0644)
			case "delete":
				os.Remove(file)
			case "executable":
				os.Chmod(file, 0755)
			case "directory":
				os.Mkdir(filepath.Join(dir, "src", "empty"), 0755)
			case "symlink":
				os.Remove(file)
				os.Symlink("/etc/passwd", file)
			}
			if err := Verify(dir, lock); err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}
func TestInputValidation(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "file"), nil, 0644)
	os.Symlink("src", filepath.Join(dir, "link"))
	for _, paths := range [][]string{nil, {"."}, {".."}, {"/etc"}, {"src/../src"}, {"src", "src/file"}, {"src", "src"}, {"link/file"}, {"missing"}, {"src\\file"}, {"a:b"}} {
		if _, err := Snapshot(dir, paths); err == nil {
			t.Fatalf("accepted %v", paths)
		}
	}
	lock, err := Snapshot(dir, []string{"src/file"})
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "lock")
	data, _ := json.Marshal(lock)
	os.WriteFile(filename, data, 0644)
	loaded, err := Read(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir, loaded); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{`{}`, `{"api_version":"invalid"}`, `{"unknown":true}`, `null`, `{} {}`} {
		os.WriteFile(filename, []byte(data), 0644)
		if _, err := Read(filename); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	os.Remove(filename)
	os.Symlink(filepath.Join(dir, "src", "file"), filename)
	if _, err := Read(filename); err == nil {
		t.Fatal("symlink lock accepted")
	}
	if _, err := Snapshot(filepath.Join(dir, "missing"), []string{"src"}); err == nil {
		t.Fatal("missing root accepted")
	}
}

func TestInputBudgets(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "large")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxFile + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if _, err := Snapshot(dir, []string{"large"}); err == nil {
		t.Fatal("accepted oversized input")
	}
	if _, err := Read(name); err == nil {
		t.Fatal("accepted oversized lock")
	}
	if _, err := Snapshot(dir, make([]string, 129)); err == nil {
		t.Fatal("accepted excessive roots")
	}
	if _, err := Read(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("accepted missing lock")
	}
}

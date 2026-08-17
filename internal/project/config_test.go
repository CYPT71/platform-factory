package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverLoadsNearestSupportedConfigAndDefaults(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, ".config_img.yml")
	writeTestFile(t, config, "version: 1\nlanguage: python\nruntime: bin/python\n", 0o644)
	child := filepath.Join(root, "src", "api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	loaded, err := Discover(child, "")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.File != config || loaded.Root != root || loaded.Config.Output != ".platform-factory/image" {
		t.Fatalf("loaded=%+v", loaded)
	}
	if loaded.Config.Isolation != "container" || loaded.Config.RuntimeEngine != "docker" {
		t.Fatalf("defaults=%+v", loaded.Config)
	}
}

func TestLegacyDisksConfigDoesNotRequireLanguageOrArtifact(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "platform-factory.yaml")
	writeTestFile(t, filename, "version: 1\nlegacy_disks:\n  boot: disk.raw\n  data:\n    - data.raw\n", 0o644)
	loaded, err := Load(filename)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Config.LegacyDisks == nil || loaded.Config.LegacyDisks.Boot != "disk.raw" {
		t.Fatalf("LegacyDisks=%+v", loaded.Config.LegacyDisks)
	}
	if loaded.Config.Language != "" || loaded.Config.Artifact != "" {
		t.Fatalf("Language=%q Artifact=%q, want both empty", loaded.Config.Language, loaded.Config.Artifact)
	}
}

func TestLoadStrictValidation(t *testing.T) {
	for name, content := range map[string]string{
		"unknown":    "language: compiled\nartifact: app\nsurprise: true\n",
		"version":    "version: 2\nlanguage: compiled\nartifact: app\n",
		"isolation":  "language: compiled\nartifact: app\nisolation: host\n",
		"missing":    "language: compiled\n",
		"platform":   "language: compiled\nartifact: app\nplatform: linux/s390x\n",
		"runtime":    "language: compiled\nartifact: app\nruntime_engine: nerdctl\n",
		"command":    "language: custom\nartifact: app\nfreeze_command: [\"\"]\n",
		"include":    "language: compiled\nartifact: app\ninclude: [{source: file, destination: relative}]\n",
		"traversal":  "language: compiled\nartifact: app\ninclude: [{source: file, destination: /app/../etc}]\n",
		"root-dest":  "language: compiled\nartifact: app\ninclude: [{source: file, destination: /}]\n",
		"nul-source": "language: compiled\nartifact: app\ninclude: [{source: \"bad\\0\", destination: /app}]\n",
		"documents":  "language: compiled\nartifact: app\n---\nlanguage: compiled\nartifact: other\n",
	} {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), ".config_image.yaml")
			writeTestFile(t, filename, content, 0o644)
			if _, err := Load(filename); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestExplicitConfigAndProjectRoot(t *testing.T) {
	parent := t.TempDir()
	configDir := filepath.Join(parent, "config")
	projectRoot := filepath.Join(parent, "source")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(configDir, "image.yaml")
	writeTestFile(t, filename, "language: compiled\nartifact: app\nproject: ../source\n", 0o644)
	loaded, err := Discover(".", filename)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Root != projectRoot || loaded.Resolve("app") != filepath.Join(projectRoot, "app") {
		t.Fatalf("loaded=%+v", loaded)
	}
}

func TestDiscoverMissingConfig(t *testing.T) {
	_, err := Discover(t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), "no project image config") {
		t.Fatalf("err=%v", err)
	}
}

func TestDiscoverValidatesAdjacentLock(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "pf.yaml"), "version: 1\nlanguage: go\nartifact: app\n", 0o600)
	writeTestFile(t, filepath.Join(dir, "pf.lock"), `{"version":2}`, 0o600)
	if _, err := Discover(dir, ""); err == nil || !strings.Contains(err.Error(), "lock version 2") {
		t.Fatalf("err=%v", err)
	}
	if err := os.Remove(filepath.Join(dir, "pf.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, "pf.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(dir, ""); err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("err=%v", err)
	}
}

func TestDiscoverLongConfigValidatesOnlyLongAdjacentLock(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "platform-factory.yaml"), "version: 1\nlanguage: go\nartifact: app\n", 0o600)
	writeTestFile(t, filepath.Join(dir, "pf.lock"), `{"version":2}`, 0o600)
	writeTestFile(t, filepath.Join(dir, "platform-factory.lock"), `{"version":1}`, 0o600)
	if _, err := Discover(dir, ""); err == nil || !strings.Contains(err.Error(), "ambiguous project locks") {
		t.Fatalf("conflicting alias lock accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "pf.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(dir, ""); err != nil {
		t.Fatalf("long project was not discovered: %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "platform-factory.lock"), `{"version":2}`, 0o600)
	if _, err := Discover(dir, ""); err == nil || !strings.Contains(err.Error(), "lock version 2") {
		t.Fatalf("long lock was not validated: %v", err)
	}
}

func TestDiscoverRejectsMultipleProjectConfigurations(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "pf.yaml"), "version: 1\nlanguage: go\nartifact: app\n", 0o600)
	writeTestFile(t, filepath.Join(dir, "platform-factory.yaml"), "version: 1\nlanguage: python\nartifact: app.py\n", 0o600)
	if _, err := Discover(dir, ""); err == nil || !strings.Contains(err.Error(), "multiple project configurations") {
		t.Fatalf("ambiguous configurations accepted: %v", err)
	}
}

func TestImageFilesIncludesProjectAndSharedDependencies(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".config_image.yaml"),
		"language: python\nruntime: runtime/python\nshared_deps:\n  - source: ../common\n    destination: /opt/common\n", 0o644)
	writeTestFile(t, filepath.Join(root, "app.py"), "print('ok')\n", 0o644)
	writeTestFile(t, filepath.Join(root, "Shared_deps", "models.dat"), "model", 0o644)
	writeTestFile(t, filepath.Join(filepath.Dir(root), "common", "cert.pem"), "cert", 0o644)
	writeTestFile(t, filepath.Join(root, ".platform-factory", "freeze.lock.json"), "old", 0o644)
	writeTestFile(t, filepath.Join(root, ".platform-factory", "image", "index.json"), "output", 0o644)
	loaded, err := Load(filepath.Join(root, ".config_image.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := loaded.ImageFiles()
	if err != nil {
		t.Fatal(err)
	}
	destinations := map[string]bool{}
	for _, file := range files {
		destinations[file.Dest] = true
	}
	for _, want := range []string{"/app/app.py", "/app/shared_deps/models.dat", "/opt/common/cert.pem"} {
		if !destinations[want] {
			t.Fatalf("missing %s in %#v", want, destinations)
		}
	}
	for _, unwanted := range []string{"/app/.platform-factory/freeze.lock.json", "/app/.platform-factory/image/index.json"} {
		if destinations[unwanted] {
			t.Fatalf("unexpected %s", unwanted)
		}
	}
	first, err := loaded.WriteFreezeInventory()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(first)
	if strings.Contains(string(before), root) {
		t.Fatalf("freeze inventory leaks absolute project path: %s", before)
	}
	if _, err := loaded.WriteFreezeInventory(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(first)
	if string(before) != string(after) {
		t.Fatal("freeze inventory is not stable")
	}
	if err := loaded.VerifyFreezeInventory(); err != nil {
		t.Fatalf("fresh inventory did not verify: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.py"), []byte("print('changed')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := loaded.VerifyFreezeInventory(); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Fatalf("changed input was accepted: %v", err)
	}
}

func TestVerifyFreezeInventoryRejectsTraversalAndUnknownFields(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "pf.yaml"), "version: 1\nlanguage: go\nartifact: app\n", 0o644)
	loaded, err := Load(filepath.Join(root, "pf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".platform-factory", "freeze.lock.json")
	writeTestFile(t, lock, `{"version":1,"language":"go","config":"pf.yaml","files":[{"path":"../outside","size":0,"sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}]}`, 0o644)
	if err := loaded.VerifyFreezeInventory(); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("traversal accepted: %v", err)
	}
	writeTestFile(t, lock, `{"version":1,"language":"go","config":"pf.yaml","files":[],"unknown":true}`, 0o644)
	if err := loaded.VerifyFreezeInventory(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field accepted: %v", err)
	}
}

func TestImageFilesAssignSemanticCategories(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".config_image.yaml"), `language: python
runtime: runtime/python
semantic_layers: true
include:
  - {source: runtime, destination: /runtime, category: toolchain}
shared_deps:
  - {source: vendor, destination: /opt/vendor}
`, 0o644)
	writeTestFile(t, filepath.Join(root, "app.py"), "print('ok')\n", 0o644)
	writeTestFile(t, filepath.Join(root, "runtime", "python"), "elf", 0o755)
	writeTestFile(t, filepath.Join(root, "vendor", "lib.py"), "lib", 0o644)
	writeTestFile(t, filepath.Join(root, "Shared_deps", "models.dat"), "model", 0o644)
	loaded, err := Load(filepath.Join(root, ".config_image.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Config.SemanticLayers {
		t.Fatal("semantic_layers not loaded")
	}
	files, err := loaded.ImageFiles()
	if err != nil {
		t.Fatal(err)
	}
	categories := map[string]string{}
	for _, file := range files {
		categories[file.Dest] = file.Category
	}
	for destination, want := range map[string]string{
		"/runtime/python":             "toolchain",
		"/opt/vendor/lib.py":          "dependencies",
		"/app/shared_deps/models.dat": "dependencies",
		"/app/app.py":                 "application",
	} {
		if categories[destination] != want {
			t.Fatalf("category(%s)=%q want %q (all: %v)", destination, categories[destination], want, categories)
		}
	}
}

func TestConfigRejectsUnknownDependencyCategory(t *testing.T) {
	filename := filepath.Join(t.TempDir(), ".config_image.yaml")
	writeTestFile(t, filename,
		"language: compiled\nartifact: app\ninclude:\n  - {source: app, destination: /data/app, category: warehouse}\n", 0o644)
	if _, err := Load(filename); err == nil || !strings.Contains(err.Error(), "category") {
		t.Fatalf("err=%v", err)
	}
}

func TestImageFilesRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".config_image.yaml"), "language: python\nruntime: python\n", 0o644)
	writeTestFile(t, filepath.Join(root, "target"), "data", 0o644)
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	loaded, err := Load(filepath.Join(root, ".config_image.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.ImageFiles(); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestImageFilesSingleFileAndDuplicateDestination(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "one"), "1", 0o600)
	writeTestFile(t, filepath.Join(root, "two"), "2", 0o644)
	writeTestFile(t, filepath.Join(root, ".config_image.yaml"), `language: compiled
artifact: one
include:
  - {source: one, destination: /data/file}
  - {source: two, destination: /data/file}
`, 0o644)
	loaded, err := Load(filepath.Join(root, ".config_image.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.ImageFiles(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err=%v", err)
	}
	loaded.Config.Include = loaded.Config.Include[:1]
	files, err := loaded.ImageFiles()
	if err != nil || len(files) != 1 || files[0].Mode != 0o600 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	loaded.Config.Include[0].Source = "missing"
	if _, err := loaded.ImageFiles(); err == nil {
		t.Fatal("expected missing source error")
	}
}

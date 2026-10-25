package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/oci"
)

func TestCIInputLockCLI(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "main"), []byte("content"), 0644)
	lock := filepath.Join(dir, "inputs.lock")
	var out, errout bytes.Buffer
	for _, args := range [][]string{{}, {"--help"}, {"lock", "--dir", dir, "--path", "src", "--output", lock}, {"verify", "--dir", dir, "--lock", lock}} {
		if code := runCI(args, &out, &errout); code != 0 {
			t.Fatalf("%v: %d %s", args, code, errout.String())
		}
	}
	for _, args := range [][]string{{"lock"}, {"lock", "--dir", dir, "--path", "src", "--output", filepath.Join(dir, "src", "lock")}, {"lock", "--dir", dir, "--path", "src", "--output", lock}, {"verify", "--dir", dir, "--lock", filepath.Join(dir, "absent")}, {"unknown"}, {"verify", "extra"}, {"verify", "--bad"}, {"package"}, {"boot"}, {"boot", "--ready", "x", "--timeout", "0s"}, {"boot", "--ready", "x", "--kernel", "missing"}} {
		if code := runCI(args, &out, &errout); code == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
	os.WriteFile(filepath.Join(dir, "src", "main"), []byte("drift"), 0644)
	if code := runCI([]string{"verify", "--dir", dir, "--lock", lock}, &out, &errout); code == 0 {
		t.Fatal("accepted drift")
	}
}
func TestCIGitLabGeneration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ci")
	var out, errout bytes.Buffer
	if code := runCI([]string{"gitlab", "--output", dir}, &out, &errout); code != 0 {
		t.Fatal(errout.String())
	}
	for _, name := range []string{"component.yml", "runner.py", "trigger.py"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if code := runCI([]string{"gitlab", "--output", dir}, &out, &errout); code == 0 {
		t.Fatal("overwrote output")
	}
	if code := runCI([]string{"gitlab"}, &out, &errout); code == 0 {
		t.Fatal("accepted empty output")
	}
}
func TestCICheckRequiresV2Lock(t *testing.T) {
	dir := t.TempDir()
	config := []byte("version: 1\nlanguage: compiled\nartifact: dist/app\nprofile: static\nuser: '65532'\n")
	os.WriteFile(filepath.Join(dir, "pf.yaml"), config, 0644)
	var out, errout bytes.Buffer
	check := func() int { return runCI([]string{"check", "--dir", dir}, &out, &errout) }
	if check() == 0 {
		t.Fatal("accepted missing lock")
	}
	os.WriteFile(filepath.Join(dir, "pf.lock"), []byte(`{"version":1}`), 0644)
	if check() == 0 {
		t.Fatal("accepted legacy lock")
	}
	digest, err := project.CanonicalManifestDigest(config)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(project.Lock{Version: 2, PlanDigest: digest})
	os.WriteFile(filepath.Join(dir, "pf.lock"), data, 0644)
	if check() == 0 {
		t.Fatal("accepted missing freeze inventory")
	}
	loaded, err := project.Load(filepath.Join(dir, "pf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.WriteFreezeInventory(); err != nil {
		t.Fatal(err)
	}
	if check() != 0 {
		t.Fatal(errout.String())
	}
	if code := runCI([]string{"check", "--dir", dir, "--distroless"}, &out, &errout); code != 0 {
		t.Fatal(errout.String())
	}
	shared := filepath.Join(dir, "shared_deps")
	os.Mkdir(shared, 0755)
	os.WriteFile(filepath.Join(shared, "shell"), []byte("forbidden extra payload"), 0755)
	if code := runCI([]string{"check", "--dir", dir, "--distroless"}, &out, &errout); code == 0 {
		t.Fatal("accepted automatically included shared dependencies")
	}
	os.RemoveAll(shared)
	os.WriteFile(filepath.Join(dir, "pf.yaml"), append(config, []byte("image: changed-plan\n")...), 0644)
	if check() == 0 {
		t.Fatal("accepted manifest drift")
	}
}

func TestCIPackagesVerifiedOCI(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "app")
	init := filepath.Join(dir, "init")
	os.WriteFile(binary, minimalCLIStaticELF(), 0755)
	os.WriteFile(init, minimalCLIStaticELF(), 0755)
	layout := filepath.Join(dir, "oci")
	if _, err := oci.Build(oci.Options{Binary: binary, Output: layout, Architecture: runtime.GOARCH, Entrypoint: "/app/service", User: "65532"}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "initramfs.cpio.gz")
	var out, errout bytes.Buffer
	args := []string{"package", "--layout", layout, "--platform", "linux/" + runtime.GOARCH, "--init", init, "--output", output}
	if code := runCI(args, &out, &errout); code != 0 {
		t.Fatalf("package: %s", errout.String())
	}
	var evidence map[string]any
	if err := json.Unmarshal(out.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence["manifest_digest"] == nil || evidence["initramfs_bytes"].(float64) <= 0 {
		t.Fatal("missing packaging evidence")
	}
	if code := runCI(args, &out, &errout); code == 0 {
		t.Fatal("overwrote existing initramfs")
	}
}

func TestCIRunnerRejectsAbsentConfiguration(t *testing.T) {
	var out, errout bytes.Buffer
	if code := runCI([]string{"run", "--config", filepath.Join(t.TempDir(), "missing"), "--phase", "build"}, &out, &errout); code == 0 {
		t.Fatal("accepted missing configuration")
	}
}
func TestGitLabHookRejectsAbsentPrivateConfiguration(t *testing.T) {
	var out, errout bytes.Buffer
	if code := runHooks([]string{"gitlab", "--config", filepath.Join(t.TempDir(), "missing")}, &out, &errout); code == 0 {
		t.Fatal("accepted missing private configuration")
	}
}

func TestCIPlanLockBootstrapAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "pf.yaml")
	os.WriteFile(config, []byte("version: 1\nlanguage: compiled\nartifact: dist/app\nprofile: static\nuser: '65532'\n"), 0644)
	loaded, err := project.Load(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.WriteFreezeInventory(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "pf.lock")
	var out, errout bytes.Buffer
	args := []string{"plan-lock", "--dir", dir, "--output", output}
	if code := runCI(args, &out, &errout); code != 0 {
		t.Fatal(errout.String())
	}
	if code := runCI([]string{"check", "--dir", dir, "--distroless"}, &out, &errout); code != 0 {
		t.Fatal(errout.String())
	}
	if code := runCI(args, &out, &errout); code == 0 {
		t.Fatal("overwrote reviewed lock")
	}
	if code := runCI([]string{"plan-lock"}, &out, &errout); code == 0 {
		t.Fatal("accepted missing output")
	}
}

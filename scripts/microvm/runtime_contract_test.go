package microvm_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProductionScriptsUseNativeOCIProcessTranslator(t *testing.T) {
	for _, name := range []string{"run-microvm.sh", "prepare-kubevirt-boot.sh"} {
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		if !strings.Contains(text, "microvm-initramfs") || strings.Contains(text, "assemble-initramfs.sh") {
			t.Fatalf("%s bypasses the native OCI process translator", name)
		}
	}
	if runtime.GOOS != "windows" {
		bash, err := exec.LookPath("bash")
		if err != nil {
			t.Skip("bash unavailable")
		}
		command := exec.Command(bash, "-n", "run-microvm.sh", "prepare-kubevirt-boot.sh")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("shell syntax: %v\n%s", err, output)
		}
	}
}

func TestModernBootManifestBindsTranslatedInitramfsAndRootFS(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	for _, name := range []string{"init", "kernel", "initramfs"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name+"-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(root, "boot.json")
	digest := "sha256:" + strings.Repeat("a", 64)
	command := exec.Command(python, filepath.Join("..", "ci", "write-microvm-boot-manifest.py"), "--architecture", "amd64", "--manifest-digest", digest, "--rootfs-digest", digest, "--initramfs", filepath.Join(root, "initramfs"), "--init", filepath.Join(root, "init"), "--kernel", filepath.Join(root, "kernel"), "--output", output)
	if produced, err := command.CombinedOutput(); err != nil {
		t.Fatalf("write modern boot manifest: %v\n%s", err, produced)
	}
	encoded, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if document["oci_manifest_digest"] != digest || document["rootfs_digest"] != digest || document["process_contract"] != "embedded:/etc/platform-factory/process.json" || document["initramfs_sha256"] == "" || document["combined_digest"] == "" {
		t.Fatalf("manifest=%v", document)
	}
}

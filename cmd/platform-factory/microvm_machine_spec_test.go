package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMicroVMMachineSpecUsesInitramfsRequirements(t *testing.T) {
	root := t.TempDir()
	requirements := filepath.Join(root, "requirements.json")
	data := `{"manifest_digest":"sha256:` + strings.Repeat("3", 64) + `","rootfs_digest":"sha256:` + strings.Repeat("4", 64) + `","files":4,"bytes":100,"initramfs_bytes":80,"required_ports":["8080/tcp"],"required_volumes":["/data"]}`
	if err := os.WriteFile(requirements, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	volume := filepath.Join(root, "data")
	if err := os.Mkdir(volume, 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"microvm", "machine-spec", "--name=demo", "--requirements=" + requirements,
		"--kernel-digest=sha256:" + strings.Repeat("1", 64), "--initramfs-digest=sha256:" + strings.Repeat("2", 64),
		"--publish=18080:8080", "--volume-source=ro@/data=" + volume, "--dns=1.1.1.1"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"guest": 8080`) || !strings.Contains(stdout.String(), `"target": "/data"`) || !strings.Contains(stdout.String(), `"platform-factory.dev/oci-manifest"`) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestMicroVMMachineSpecFailsClosed(t *testing.T) {
	root := t.TempDir()
	requirements := filepath.Join(root, "requirements.json")
	digest := "sha256:" + strings.Repeat("3", 64)
	if err := os.WriteFile(requirements, []byte(`{"manifest_digest":"`+digest+`","rootfs_digest":"`+digest+`","files":1,"bytes":1,"initramfs_bytes":1,"required_ports":["80/tcp"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"microvm", "machine-spec", "--requirements=" + requirements, "--kernel-digest=" + digest, "--initramfs-digest=" + digest}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "no explicit forwarding") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	unknown := filepath.Join(root, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"manifest_digest":"`+digest+`","rootfs_digest":"`+digest+`","files":1,"bytes":1,"initramfs_bytes":1,"surprise":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	code = run([]string{"microvm", "machine-spec", "--requirements=" + unknown, "--kernel-digest=" + digest, "--initramfs-digest=" + digest}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

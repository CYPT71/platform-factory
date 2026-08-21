package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/microvm"
	"github.com/CYPT71/platform-factory/internal/networking"
	"github.com/CYPT71/platform-factory/internal/rootfs"
)

func writeNativeBlob(t *testing.T, layout string, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	hexDigest := hex.EncodeToString(sum[:])
	dir := filepath.Join(layout, "blobs", "sha256")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hexDigest), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return "sha256:" + hexDigest
}

func TestReadEntrypointVerifiesBothBlobs(t *testing.T) {
	layout := t.TempDir()
	configDigest := writeNativeBlob(t, layout, []byte(`{"config":{"Entrypoint":["/app/service","--serve"]}}`))
	manifestDigest := writeNativeBlob(t, layout, []byte(`{"config":{"digest":"`+configDigest+`"}}`))

	got, err := readEntrypoint(layout, manifestDigest)
	if err != nil || !reflect.DeepEqual(got, []string{"/app/service", "--serve"}) {
		t.Fatalf("entrypoint=%v err=%v", got, err)
	}

	manifestHex := strings.TrimPrefix(manifestDigest, "sha256:")
	if err := os.WriteFile(filepath.Join(layout, "blobs", "sha256", manifestHex), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readEntrypoint(layout, manifestDigest); err == nil || !strings.Contains(err.Error(), "digest verification") {
		t.Fatalf("tampered manifest accepted: %v", err)
	}
}

func TestReadEntrypointRejectsMalformedAndEmptyDocuments(t *testing.T) {
	for _, test := range []struct {
		name, manifest, config, want string
	}{
		{"bad-manifest", `{`, ``, "decode manifest"},
		{"bad-config-digest", `{"config":{"digest":"md5:bad"}}`, ``, "unsupported digest"},
		{"bad-config", ``, `{`, "decode image config"},
		{"empty-entrypoint", ``, `{"config":{"Entrypoint":[]}}`, "no Entrypoint"},
	} {
		t.Run(test.name, func(t *testing.T) {
			layout := t.TempDir()
			configDigest := "sha256:missing"
			if test.config != "" {
				configDigest = writeNativeBlob(t, layout, []byte(test.config))
			}
			manifest := test.manifest
			if manifest == "" {
				manifest = `{"config":{"digest":"` + configDigest + `"}}`
			}
			manifestDigest := writeNativeBlob(t, layout, []byte(manifest))
			_, err := readEntrypoint(layout, manifestDigest)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v, want %q", err, test.want)
			}
		})
	}
}

func TestReadVerifiedBlobRejectsInvalidOrMissingDigest(t *testing.T) {
	for _, digest := range []string{"", "sha512:abc", "sha256:", "sha256:missing"} {
		if _, err := readVerifiedBlob(t.TempDir(), digest); err == nil {
			t.Fatalf("digest %q accepted", digest)
		}
	}
}

func TestInstallNativeRuntimeContractTranslatesOrRefusesEveryOCIRequirement(t *testing.T) {
	root := t.TempDir()
	initBinary := filepath.Join(t.TempDir(), "init")
	if err := os.WriteFile(initBinary, []byte("init"), 0o500); err != nil {
		t.Fatal(err)
	}
	metadata := rootfs.RuntimeMetadata{Process: rootfs.ProcessConfig{Args: []string{"/app/service", "--serve"}, Env: []string{"MODE=production"}, Cwd: "/app", UID: 1000, GID: 1001}, Ports: []string{"8080/tcp"}}
	forwards := []networking.Forward{{Protocol: "tcp", HostPort: 18080, GuestPort: 8080}}
	if err := installNativeRuntimeContract(root, initBinary, metadata, forwards); err != nil {
		t.Fatal(err)
	}
	process, err := os.ReadFile(filepath.Join(root, "etc", "platform-factory", "process.json"))
	if err != nil || !strings.Contains(string(process), `"MODE=production"`) || !strings.Contains(string(process), `"uid":1000`) {
		t.Fatalf("process=%s err=%v", process, err)
	}
	for _, invalid := range []rootfs.RuntimeMetadata{
		{Process: metadata.Process, Ports: []string{"8443/tcp"}},
		{Process: metadata.Process, Volumes: []string{"/data"}},
		{Process: metadata.Process, UnsupportedOptions: []string{"Healthcheck"}},
	} {
		if err := installNativeRuntimeContract(t.TempDir(), initBinary, invalid, forwards); err == nil {
			t.Fatalf("untranslated OCI requirement accepted: %+v", invalid)
		}
	}
}

func TestNativeRunArgsAreStableAndPreserveForwards(t *testing.T) {
	spec := microvm.Spec{Layout: "/layout", MemoryMiB: 384, Listen: "127.0.0.1", Forwards: []networking.Forward{
		{Protocol: "tcp", HostPort: 8080, GuestPort: 80},
		{Protocol: "udp", HostIP: "::1", HostPort: 5353, GuestPort: 53},
	}}
	want := []string{"microvm", "__run-native", "--layout", "/layout", "--memory-mib", "384", "--publish", "tcp|127.0.0.1|8080|80", "--publish", "udp|::1|5353|53"}
	if got := nativeRunArgs(spec); !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%v, want %v", got, want)
	}
}

func TestRunNativeKVMSubcommandRejectsMalformedInput(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"flag", []string{"--unknown"}, "flag provided"},
		{"missing-layout", nil, "--layout is required"},
		{"publish-shape", []string{"--layout", "/layout", "--publish", "tcp|127.0.0.1|80"}, "malformed --publish"},
		{"host-port", []string{"--layout", "/layout", "--publish", "tcp|127.0.0.1|bad|80"}, "malformed --publish"},
		{"guest-port", []string{"--layout", "/layout", "--publish", "tcp|127.0.0.1|80|bad"}, "malformed --publish"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runNativeKVMSubcommand(test.args, &stdout, &stderr); code != 2 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr=%q, want %q", stderr.String(), test.want)
			}
		})
	}
}

func TestRunNativeKVMSubcommandReportsPreparationFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runNativeKVMSubcommand([]string{
		"--layout", filepath.Join(t.TempDir(), "missing"),
		"--memory-mib", "256",
		"--publish", "tcp|127.0.0.1|8080|80",
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "microvm run (native KVM)") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestNativeLogIsStructured(t *testing.T) {
	var output bytes.Buffer
	nativeLog(&output, "phase=%s count=%d", "test", 2)
	if !strings.Contains(output.String(), "] phase=test count=2\n") || !strings.HasPrefix(output.String(), "[") {
		t.Fatalf("log=%q", output.String())
	}
}

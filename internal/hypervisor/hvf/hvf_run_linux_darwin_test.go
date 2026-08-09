//go:build darwin && cgo

package hvf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	api "github.com/CYPT71/secure-oci-base/internal/microvm"
	vmruntime "github.com/CYPT71/secure-oci-base/internal/runtime"
)

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestRunLinuxWithRealHVF is the native macOS equivalent of
// TestRunLinuxWithRealKVM. It is opt-in because it needs a matching raw Linux
// kernel Image and a process allowed to use Apple's virtualization stack.
func TestRunLinuxWithRealHVF(t *testing.T) {
	kernelPath := os.Getenv("PLATFORM_FACTORY_TEST_KERNEL_IMAGE")
	if kernelPath == "" {
		t.Skip("PLATFORM_FACTORY_TEST_KERNEL_IMAGE is not set")
	}
	initrdPath := os.Getenv("PLATFORM_FACTORY_TEST_INITRD")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := RunLinuxHVF(ctx, kernelPath, initrdPath,
		"console=hvc0 earlycon=hvc0 rdinit=/sbin/init ignore_loglevel panic=0",
		`"component":"example-service"`, 512<<20, 1, "", nil)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run Linux/%s with HVF: %v; serial=%q", runtime.GOARCH, err, result.Serial)
	}
	if len(result.Serial) == 0 {
		t.Fatalf("kernel produced no serial boot diagnostics (stopped=%t)", result.Stopped)
	}
	if initrdPath != "" && (!result.SerialMatched ||
		!strings.Contains(string(result.Serial), `"component":"example-service"`)) {
		t.Fatalf("OCI application did not start in the native macOS guest; serial=%q", result.Serial)
	}
}

// TestDarwinVMMWithRealHVF exercises the public VMM lifecycle with the
// project-owned OCI initramfs as the bundle's verified rootfs.
func TestDarwinVMMWithRealHVF(t *testing.T) {
	kernelPath := os.Getenv("PLATFORM_FACTORY_TEST_KERNEL_IMAGE")
	rootfsPath := os.Getenv("PLATFORM_FACTORY_TEST_INITRD")
	if kernelPath == "" || rootfsPath == "" {
		t.Skip("PLATFORM_FACTORY_TEST_KERNEL_IMAGE and PLATFORM_FACTORY_TEST_INITRD are not set")
	}
	kernelDigest := fileDigest(t, kernelPath)
	rootfsDigest := fileDigest(t, rootfsPath)
	paths := map[string]string{kernelDigest: kernelPath, rootfsDigest: rootfsPath}
	resolve := func(_ context.Context, digest string) (string, error) {
		path, ok := paths[digest]
		if !ok {
			return "", errors.New("unexpected content digest")
		}
		return path, nil
	}
	backend, err := NewDarwinVMM(resolve, filepath.Join(t.TempDir(), "logs"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := vmruntime.NewBootBundle(kernelDigest, "", rootfsDigest,
		[]string{"console=hvc0", "earlycon=hvc0", "rdinit=/sbin/init", "ignore_loglevel", "panic=0"},
		map[string]string{darwinRootFSFormatKey: darwinRootFSFormatInitramfs})
	if err != nil {
		t.Fatal(err)
	}
	machine, err := backend.Create(context.Background(), api.MachineSpec{
		ID: "real-hvf-lifecycle", Bundle: bundle,
		Resources: api.Resources{VCPUs: 1, MemoryMiB: 512},
	})
	if err != nil {
		t.Fatalf("create Linux/%s machine: %v", runtime.GOARCH, err)
	}
	if err := machine.Start(context.Background()); err != nil {
		t.Fatalf("start Linux/%s machine: %v", runtime.GOARCH, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	const ready = `"component":"example-service"`
	var serial bytes.Buffer
	for !bytes.Contains(serial.Bytes(), []byte(ready)) {
		serial.Reset()
		if err := machine.Logs(ctx, &serial); err != nil {
			t.Fatalf("read serial logs: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("OCI application did not start: %v; serial=%q", ctx.Err(), serial.Bytes())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := machine.Stop(context.Background()); err != nil {
		t.Fatalf("stop machine: %v; serial=%q", err, serial.Bytes())
	}
	if err := backend.Delete(context.Background(), machine.ID()); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	if _, err := backend.Load(context.Background(), machine.ID()); err == nil {
		t.Fatal("deleted machine remained loadable")
	}
}

func TestRunLinuxHVFRejectsInvalidInputsBeforeFrameworkCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunLinuxHVF(ctx, "", "", "", "", 0, 0, "", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled context err=%v", err)
	}
	if _, err := RunLinuxHVF(context.Background(), "", "", "", "", 512<<20, 1, "", nil); err == nil {
		t.Fatal("missing kernel path accepted")
	}
	missing := t.TempDir() + "/missing"
	if _, err := RunLinuxHVF(context.Background(), missing, "", "", "", 512<<20, 1, "", nil); err == nil {
		t.Fatal("missing kernel file accepted")
	}
	kernel := t.TempDir() + "/Image"
	if err := os.WriteFile(kernel, []byte("kernel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunLinuxHVF(context.Background(), kernel, "", "", "", 0, 1, "", nil); err == nil {
		t.Fatal("undersized memory accepted")
	}
	if _, err := RunLinuxHVF(context.Background(), kernel, missing, "", "", 512<<20, 1, "", nil); err == nil {
		t.Fatal("missing initramfs accepted")
	}
}

// TestRunLinuxHVFRejectsMalformedMACAddress proves the mac_address
// validation in vz_create_machine runs (and fails closed) before any
// Virtualization.framework call that would need the
// com.apple.security.virtualization entitlement this test binary does
// not have - so this assertion holds even in an unsigned/unentitled
// environment like CI or this sandbox, unlike TestRunLinuxWithRealHVF.
func TestRunLinuxHVFRejectsMalformedMACAddress(t *testing.T) {
	kernel := t.TempDir() + "/Image"
	if err := os.WriteFile(kernel, []byte("kernel"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := RunLinuxHVF(context.Background(), kernel, "", "", "", 512<<20, 1, "not-a-mac-address", nil)
	if err == nil || !strings.Contains(err.Error(), "mac_address") {
		t.Fatalf("err=%v, want a mac_address validation error", err)
	}
}

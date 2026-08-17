//go:build linux && amd64

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/oci"
)

// TestMicroVMRunNativeKVMRealBoot is the real, end-to-end counterpart to
// TestRunMicroVMNativeKVMDispatch's in-process assertions: it builds a
// real OCI layout around the project's own example-service binary, builds
// the real platform-factory CLI, runs `platform-factory microvm run --backend=native
// --layout ... --publish ...` as a genuine subprocess (exercising the
// real self re-exec into `microvm __run-native`, not a fake executor),
// and asserts a real HTTP request through the published port reaches the
// guest's actual HTTP server and gets its actual response - the same
// "prove it for real" standard the virtio-blk/virtio-net work earlier in
// this project's history was held to, applied here to the CLI dispatch
// path that now sits in front of them.
//
// Skips cleanly without native KVM. Needs to run as root (or with
// CAP_NET_ADMIN and an owned network namespace): the native path's TAP
// device requires it, the same requirement OpenTAP/ProbeTAPSupport
// document.
func TestMicroVMRunNativeKVMRealBoot(t *testing.T) {
	if !nativeKVMAvailableForTest(t) {
		t.Skip("no native KVM on this host")
	}
	if os.Geteuid() != 0 {
		t.Skip("native KVM networking (TAP) requires root; rerun as root to exercise this test")
	}

	root := t.TempDir()

	serviceBinary := filepath.Join(root, "example-service")
	buildService := exec.Command("go", "build", "-o", serviceBinary, "./cmd/example-service")
	buildService.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := buildService.CombinedOutput(); err != nil {
		t.Fatalf("build example-service: %v: %s", err, out)
	}

	layoutDir := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{Binary: serviceBinary, Output: layoutDir}); err != nil {
		t.Fatalf("build OCI layout: %v", err)
	}

	// Build the real CLI binary rather than calling runMicroVM in-process:
	// the whole point of this test is to prove the actual dispatch path
	// (runNative -> nativeKVMEligible -> self re-exec via os.Executable()
	// -> `microvm __run-native`), which only a real separate process can
	// exercise.
	cliBinary := filepath.Join(root, "platform-factory")
	if out, err := exec.Command("go", "build", "-o", cliBinary, "./cmd/platform-factory").CombinedOutput(); err != nil {
		t.Fatalf("build platform-factory: %v: %s", err, out)
	}

	hostPort := freeTCPPort(t)
	cmd := exec.Command(cliBinary, "microvm", "run",
		"--backend=native", "--layout="+layoutDir,
		"--publish", fmt.Sprintf("127.0.0.1:%d:8080", hostPort),
	)
	// A new process group so a single signal to -pgid reaches both this
	// process and the `__run-native` child it re-execs into - runNative's
	// production executor (executeMicroVMCommand) never sets its own
	// Setpgid, so the child inherits this group rather than starting a
	// new one.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start platform-factory microvm run: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopGroup := func(signal syscall.Signal) {
		_ = syscall.Kill(-cmd.Process.Pid, signal)
	}

	resp, err := waitForHTTPResponse(fmt.Sprintf("http://127.0.0.1:%d/healthz", hostPort), 120*time.Second)
	if err != nil {
		stopGroup(syscall.SIGKILL)
		<-done
		t.Fatalf("never got a response through the published port: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	status := resp.StatusCode

	stopGroup(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		stopGroup(syscall.SIGKILL)
		<-done
		t.Fatalf("platform-factory microvm run did not exit within 30s of SIGTERM; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	if readErr != nil {
		t.Fatalf("read response body: %v", readErr)
	}
	if status != http.StatusOK || string(body) != "ok\n" {
		t.Fatalf("unexpected response status=%d body=%q; stdout=%s stderr=%s", status, body, stdout.String(), stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("using native KVM backend")) {
		t.Fatalf("expected the native-KVM-backend log line on stderr; stderr=%s", stderr.String())
	}
}

func waitForHTTPResponse(url string, timeout time.Duration) (*http.Response, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	return nil, lastErr
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

//go:build darwin && cgo

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/networking"
	"github.com/CYPT71/platform-factory/oci"
)

// TestNativeHVFRealTCPAndUDP boots the actual Linux/arm64 OCI workload under
// Apple's native virtualization stack, waits for guest DHCP, then proves both
// host TCP and UDP forwarding reach the workload. The compiled test binary
// must carry com.apple.security.virtualization; test-hvf-network-local.sh owns
// that explicit signing step.
func TestNativeHVFRealTCPAndUDP(t *testing.T) {
	if os.Getenv("PLATFORM_FACTORY_TEST_HVF_NETWORK") != "1" {
		t.Skip("PLATFORM_FACTORY_TEST_HVF_NETWORK is not set")
	}
	root := t.TempDir()
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	service := filepath.Join(root, "example-service")
	build := exec.Command("go", "build", "-trimpath", "-o", service, "./cmd/example-service")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Linux/arm64 service: %v: %s", err, output)
	}
	layout := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{Binary: service, Output: layout, Architecture: "arm64"}); err != nil {
		t.Fatalf("build OCI layout: %v", err)
	}
	tcpPort := reserveTCPPort(t)
	udpPort := reserveUDPPort(t)
	forwards := []networking.Forward{
		{Protocol: "tcp", HostIP: "127.0.0.1", HostPort: tcpPort, GuestPort: 8080},
		{Protocol: "udp", HostIP: "127.0.0.1", HostPort: udpPort, GuestPort: 8053},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runNativeKVM(ctx, layout, 512, forwards, &stdout, &stderr) }()

	response, err := waitHVFHTTP(fmt.Sprintf("http://127.0.0.1:%d/healthz", tcpPort), 60*time.Second)
	if err != nil {
		cancel()
		<-done
		t.Fatalf("TCP did not reach HVF guest: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "ok\n" {
		cancel()
		<-done
		t.Fatalf("unexpected TCP response status=%d body=%q err=%v", response.StatusCode, body, readErr)
	}
	udpResponse, err := waitHVFUDP(udpPort, []byte("apple hvf udp"), 15*time.Second)
	if err != nil || string(udpResponse) != "APPLE HVF UDP" {
		cancel()
		<-done
		t.Fatalf("UDP did not reach HVF guest: response=%q err=%v; stdout=%s stderr=%s", udpResponse, err, stdout.String(), stderr.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stop HVF guest: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HVF guest did not stop after cancellation")
	}
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func reserveUDPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.LocalAddr().(*net.UDPAddr).Port
}

func waitHVFHTTP(url string, timeout time.Duration) (*http.Response, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			return response, nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return nil, lastErr
}

func waitHVFUDP(port int, payload []byte, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	address := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	for time.Now().Before(deadline) {
		connection, err := net.DialUDP("udp", nil, address)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		_ = connection.SetDeadline(time.Now().Add(300 * time.Millisecond))
		_, writeErr := connection.Write(payload)
		response := make([]byte, 65535)
		n, readErr := connection.Read(response)
		connection.Close()
		if writeErr == nil && readErr == nil {
			return response[:n], nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out waiting for UDP response on port %d", port)
}

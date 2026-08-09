package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/guesttransport"
)

func TestGuestEndpointServesAuthenticatedLifecycle(t *testing.T) {
	host, guest := net.Pipe()
	key := bytes.Repeat([]byte{7}, 32)
	var signaled os.Signal
	shutdown := false
	handler := guestEndpointHandler{
		exec: func(_ context.Context, args []string, stdin []byte) (int, []byte, []byte, error) {
			if args[0] != "/bin/app" || string(stdin) != "input" {
				t.Fatalf("exec args=%v stdin=%q", args, stdin)
			}
			return 9, []byte("out"), []byte("err"), nil
		},
		signal: func(signal os.Signal) error { signaled = signal; return nil },
		state:  func() string { return "running" },
		logs:   func() ([]byte, error) { return []byte("logs"), nil },
		shutdown: func() error {
			shutdown = true
			return nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- serveGuestEndpoint(context.Background(), guest, key, handler) }()
	codec, err := guesttransport.NewCodec(host, host, key)
	if err != nil {
		t.Fatal(err)
	}
	client := guesttransport.NewClient(codec)
	response, err := client.Do(context.Background(), guesttransport.Request{
		Operation: guesttransport.OpExec, Args: []string{"/bin/app"}, Stdin: []byte("input"),
	})
	if err != nil || response.ExitCode != 9 || string(response.Stdout) != "out" || string(response.Stderr) != "err" {
		t.Fatalf("exec response=%+v err=%v", response, err)
	}
	if _, err := client.Do(context.Background(), guesttransport.Request{Operation: guesttransport.OpSignal, Signal: "SIGTERM"}); err != nil {
		t.Fatal(err)
	}
	response, err = client.Do(context.Background(), guesttransport.Request{Operation: guesttransport.OpState})
	if err != nil || response.State != "running" {
		t.Fatalf("state response=%+v err=%v", response, err)
	}
	response, err = client.Do(context.Background(), guesttransport.Request{Operation: guesttransport.OpLogs})
	if err != nil || string(response.Logs) != "logs" {
		t.Fatalf("logs response=%+v err=%v", response, err)
	}
	if _, err := client.Do(context.Background(), guesttransport.Request{Operation: guesttransport.OpShutdown}); err != nil {
		t.Fatal(err)
	}
	if signaled != syscall.SIGTERM || !shutdown {
		t.Fatalf("signal=%v shutdown=%t", signaled, shutdown)
	}
	_ = host.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGuestHandlerFailsClosedAndBoundsResponses(t *testing.T) {
	handler := guestEndpointHandler{
		exec: func(context.Context, []string, []byte) (int, []byte, []byte, error) {
			large := bytes.Repeat([]byte("x"), maxGuestDiagnosticBytes+10)
			return 1, large, large, errors.New(strings.Repeat("e", maxGuestDiagnosticBytes+10))
		},
		logs: func() ([]byte, error) {
			return bytes.Repeat([]byte("l"), maxGuestDiagnosticBytes+1), nil
		},
	}
	for _, request := range []guesttransport.Request{
		{Operation: guesttransport.OpExec, Args: []string{"relative"}},
		{Operation: guesttransport.OpExec, Args: []string{"/bin/a\x00b"}},
		{Operation: guesttransport.OpSignal, Signal: "NOPE"},
		{Operation: guesttransport.OpState},
		{Operation: guesttransport.OpShutdown},
	} {
		response := handler.Handle(context.Background(), request)
		if response.Error == "" {
			t.Fatalf("request %+v did not fail closed", request)
		}
	}
	response := handler.Handle(context.Background(), guesttransport.Request{Operation: guesttransport.OpExec, Args: []string{"/bin/app"}})
	if len(response.Stdout) != maxGuestDiagnosticBytes || len(response.Stderr) != maxGuestDiagnosticBytes ||
		len(response.Error) != maxGuestDiagnosticBytes {
		t.Fatalf("unbounded exec response: stdout=%d stderr=%d error=%d", len(response.Stdout), len(response.Stderr), len(response.Error))
	}
	response = handler.Handle(context.Background(), guesttransport.Request{Operation: guesttransport.OpLogs})
	if len(response.Logs) != maxGuestDiagnosticBytes {
		t.Fatalf("logs length=%d", len(response.Logs))
	}
}

func TestLoadGuestSessionKeyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{4}, 32)
	for name, encoded := range map[string]string{
		"hex":    hex.EncodeToString(key),
		"base64": base64.StdEncoding.EncodeToString(key),
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := loadGuestSessionKey(path)
		if err != nil || !bytes.Equal(got, key) {
			t.Fatalf("%s key=%x err=%v", name, got, err)
		}
	}
	for name, encoded := range map[string]string{"empty": "", "weak": hex.EncodeToString([]byte("weak")), "invalid": "not key material"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadGuestSessionKey(path); err == nil {
			t.Fatalf("%s key accepted", name)
		}
	}
	if _, err := loadGuestSessionKey(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := loadGuestSessionKey(dir); err == nil {
		t.Fatal("directory key accepted")
	}
	publicPath := filepath.Join(dir, "public")
	if err := os.WriteFile(publicPath, []byte(hex.EncodeToString(key)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGuestSessionKey(publicPath); err == nil {
		t.Fatal("group/world-readable key accepted")
	}
}

func TestRunGuestCommandCapturesExitAndBoundsOutput(t *testing.T) {
	code, stdout, stderr, err := runGuestCommand(context.Background(),
		[]string{"/bin/sh", "-c", `printf out; printf err >&2; exit 6`}, nil)
	if err != nil || code != 6 || string(stdout) != "out" || string(stderr) != "err" {
		t.Fatalf("code=%d stdout=%q stderr=%q err=%v", code, stdout, stderr, err)
	}
	code, stdout, _, err = runGuestCommand(context.Background(),
		[]string{"/bin/sh", "-c", `yes x | head -c 300000`}, nil)
	if err != nil || code != 0 || len(stdout) != maxGuestDiagnosticBytes {
		t.Fatalf("bounded command code=%d stdout=%d err=%v", code, len(stdout), err)
	}
}

func TestServeGuestEndpointRejectsMissingConnectionAndWeakKey(t *testing.T) {
	if err := serveGuestEndpoint(context.Background(), nil, bytes.Repeat([]byte{1}, 32), guestEndpointHandler{}); err == nil {
		t.Fatal("nil connection accepted")
	}
	host, guest := net.Pipe()
	defer host.Close()
	if err := serveGuestEndpoint(context.Background(), guest, []byte("weak"), guestEndpointHandler{}); err == nil {
		t.Fatal("weak key accepted")
	}
}

func TestServeGuestEndpointCancellationInterruptsRead(t *testing.T) {
	host, guest := net.Pipe()
	defer host.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveGuestEndpoint(ctx, guest, bytes.Repeat([]byte{2}, 32), guestEndpointHandler{})
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

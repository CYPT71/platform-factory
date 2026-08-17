package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/guesttransport"
)

func TestOptionalGuestEndpointIsDisabledByDefault(t *testing.T) {
	child := newFakeChild()
	close(child.exitAfter)
	opened := false
	var stdout, stderr bytes.Buffer
	code := runWithOptionalGuestEndpoint(child, make(chan os.Signal), &stdout, &stderr, func() error { return nil },
		func(string) (guestEndpointConfig, error) { return guestEndpointConfig{}, os.ErrNotExist },
		func(string) ([]byte, error) { t.Fatal("key loaded without opt-in"); return nil, nil },
		func(string, int, os.FileMode) (io.ReadWriteCloser, error) {
			opened = true
			return nil, errors.New("must not open")
		})
	if code != 0 || opened || strings.Contains(stderr.String(), "guest-agent") {
		t.Fatalf("code=%d opened=%t stderr=%q", code, opened, stderr.String())
	}
}

func TestOptedInGuestEndpointControlsTrackedChild(t *testing.T) {
	host, guest := net.Pipe()
	key := bytes.Repeat([]byte{8}, 32)
	child := newFakeChild()
	var closeOnce sync.Once
	opened := make(chan struct{})
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		done <- runWithOptionalGuestEndpoint(child, make(chan os.Signal), io.Discard, &stderr, func() error { return nil },
			func(path string) (guestEndpointConfig, error) {
				if path != guestAgentConfigPath {
					t.Errorf("config path=%q", path)
				}
				return guestEndpointConfig{Device: guestAgentDevice, SessionKeyPath: guestAgentKeyPath}, nil
			},
			func(path string) ([]byte, error) {
				if path != guestAgentKeyPath {
					t.Errorf("key path=%q", path)
				}
				return append([]byte(nil), key...), nil
			},
			func(path string, flags int, _ os.FileMode) (io.ReadWriteCloser, error) {
				if path != guestAgentDevice || flags != os.O_RDWR {
					t.Errorf("open path=%q flags=%d", path, flags)
				}
				close(opened)
				return guest, nil
			})
	}()
	<-opened
	codec, err := guesttransport.NewCodec(host, host, key)
	if err != nil {
		t.Fatal(err)
	}
	client := guesttransport.NewClient(codec)
	var response guesttransport.Response
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response, err = client.Do(context.Background(), guesttransport.Request{Operation: guesttransport.OpState})
		if err == nil && response.State == "running" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil || response.State != "running" {
		closeOnce.Do(func() { close(child.exitAfter) })
		<-done
		t.Fatalf("state=%q err=%v stderr=%q", response.State, err, stderr.String())
	}
	if _, err := client.Do(context.Background(), guesttransport.Request{Operation: guesttransport.OpSignal, Signal: "TERM"}); err != nil {
		t.Fatal(err)
	}
	closeOnce.Do(func() { close(child.exitAfter) })
	if code := <-done; code != 0 {
		t.Fatalf("exit code=%d", code)
	}
	_ = host.Close()
	if len(child.signals) != 1 || child.signals[0] != syscall.SIGTERM {
		t.Fatalf("signals=%v", child.signals)
	}
}

func TestOptedInEndpointFailureDoesNotBreakEntrypoint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		loadKey func(string) ([]byte, error)
		open    openGuestDeviceFunc
	}{
		{
			name:    "key",
			loadKey: func(string) ([]byte, error) { return nil, errors.New("missing boot key") },
			open: func(string, int, os.FileMode) (io.ReadWriteCloser, error) {
				t.Fatal("device opened after key failure")
				return nil, nil
			},
		},
		{
			name:    "device",
			loadKey: func(string) ([]byte, error) { return bytes.Repeat([]byte{1}, 32), nil },
			open: func(string, int, os.FileMode) (io.ReadWriteCloser, error) {
				return nil, errors.New("ttyS1 unavailable")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := newFakeChild()
			child.exitCode = 17
			close(child.exitAfter)
			var stderr bytes.Buffer
			code := runWithOptionalGuestEndpoint(child, make(chan os.Signal), io.Discard, &stderr, func() error { return nil },
				func(string) (guestEndpointConfig, error) {
					return guestEndpointConfig{Device: guestAgentDevice, SessionKeyPath: guestAgentKeyPath}, nil
				}, tc.loadKey, tc.open)
			if code != 17 || !child.started || !strings.Contains(stderr.String(), "phase=activate") {
				t.Fatalf("code=%d started=%t stderr=%q", code, child.started, stderr.String())
			}
		})
	}
}

func TestRuntimeGuestLogsAreBoundedAndStateTracksFailure(t *testing.T) {
	child := newFakeChild()
	child.startErr = errors.New("start failed")
	tracked := &trackedGuestChild{child: child, state: "created"}
	if err := tracked.Start(); err == nil || tracked.State() != "failed" {
		t.Fatalf("start err=%v state=%q", err, tracked.State())
	}
	logs := &concurrentBoundedBuffer{}
	_, _ = logs.Write(bytes.Repeat([]byte("x"), maxGuestDiagnosticBytes+20))
	response := runtimeGuestHandler(tracked, logs).Handle(context.Background(),
		guesttransport.Request{Operation: guesttransport.OpLogs})
	if len(response.Logs) != maxGuestDiagnosticBytes {
		t.Fatalf("logs length=%d", len(response.Logs))
	}
}

func TestLoadGuestEndpointConfigIsStrictAndPinned(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(valid, []byte(`{"device":"/dev/ttyS1","session_key_path":"/etc/platform-factory/guest-session.key"}`), 0o444); err != nil {
		t.Fatal(err)
	}
	config, err := loadGuestEndpointConfig(valid)
	if err != nil || config.Device != guestAgentDevice || config.SessionKeyPath != guestAgentKeyPath {
		t.Fatalf("config=%+v err=%v", config, err)
	}
	for name, content := range map[string]string{
		"device":   `{"device":"/dev/ttyS0","session_key_path":"/etc/platform-factory/guest-session.key"}`,
		"key":      `{"device":"/dev/ttyS1","session_key_path":"/tmp/key"}`,
		"unknown":  `{"device":"/dev/ttyS1","session_key_path":"/etc/platform-factory/guest-session.key","extra":true}`,
		"trailing": `{"device":"/dev/ttyS1","session_key_path":"/etc/platform-factory/guest-session.key"} {}`,
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, []byte(content), 0o444); err != nil {
			t.Fatal(err)
		}
		if _, err := loadGuestEndpointConfig(path); err == nil {
			t.Fatalf("%s config accepted", name)
		}
	}
}

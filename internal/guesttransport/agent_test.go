package guesttransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAgentImplementsGuestAgentAndExtendedControl(t *testing.T) {
	host, guest := net.Pipe()
	agent, err := NewAgent(host, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	guestCodec, _ := NewCodec(guest, guest, testKey)
	requests := make(chan Request, 6)
	done := make(chan error, 1)
	go func() {
		defer guest.Close()
		handler := handlerFunc(func(_ context.Context, request Request) Response {
			requests <- request
			switch request.Operation {
			case OpExec:
				return Response{ExitCode: 23, Stdout: []byte("output"), Stderr: []byte("warning")}
			case OpState:
				return Response{State: "running"}
			case OpLogs:
				return Response{Logs: []byte("guest log")}
			default:
				return Response{}
			}
		})
		for range 6 {
			if err := ServeOne(context.Background(), guestCodec, handler); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	var stdout, stderr bytes.Buffer
	exitCode, err := agent.Exec(context.Background(), []string{"/bin/app"}, strings.NewReader("stdin"), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 23 || stdout.String() != "output" || stderr.String() != "warning" {
		t.Fatalf("exec = code %d stdout %q stderr %q", exitCode, stdout.String(), stderr.String())
	}
	if err := agent.Signal(context.Background(), "TERM"); err != nil {
		t.Fatal(err)
	}
	state, err := agent.State(context.Background())
	if err != nil || state != "running" {
		t.Fatalf("state = %q, %v", state, err)
	}
	var logs bytes.Buffer
	if err := agent.Logs(context.Background(), &logs); err != nil || logs.String() != "guest log" {
		t.Fatalf("logs = %q, %v", logs.String(), err)
	}
	if err := agent.Logs(context.Background(), nil); err != nil {
		t.Fatalf("discard logs: %v", err)
	}
	if err := agent.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(requests)
	var got []Request
	for request := range requests {
		got = append(got, request)
	}
	if len(got) != 6 || string(got[0].Stdin) != "stdin" || got[1].Signal != "TERM" {
		t.Fatalf("requests = %+v", got)
	}
}

func TestAgentBoundsInputAndPropagatesOutputErrors(t *testing.T) {
	host, guest := net.Pipe()
	agent, err := NewAgent(host, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	defer guest.Close()
	tooLarge := io.LimitReader(zeroReader{}, MaxIOBytes+1)
	if _, err := agent.Exec(context.Background(), []string{"app"}, tooLarge, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "768 KiB") {
		t.Fatalf("large stdin error = %v", err)
	}

	guestCodec, _ := NewCodec(guest, guest, testKey)
	go func() {
		_ = ServeOne(context.Background(), guestCodec, handlerFunc(func(context.Context, Request) Response {
			return Response{Stdout: []byte("out")}
		}))
	}()
	_, err = agent.Exec(context.Background(), []string{"app"}, nil, failingWriter{}, nil)
	if err == nil || !strings.Contains(err.Error(), "write stdout") {
		t.Fatalf("writer error = %v", err)
	}
}

func TestAgentPropagatesInputAndLogWriterErrors(t *testing.T) {
	host, guest := net.Pipe()
	agent, err := NewAgent(host, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	defer guest.Close()
	if _, err := agent.Exec(context.Background(), []string{"app"}, failingReader{}, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "read stdin") {
		t.Fatalf("reader error = %v", err)
	}
	guestCodec, _ := NewCodec(guest, guest, testKey)
	go func() {
		_ = ServeOne(context.Background(), guestCodec, handlerFunc(func(context.Context, Request) Response {
			return Response{Logs: []byte("log")}
		}))
	}()
	if err := agent.Logs(context.Background(), failingWriter{}); err == nil || !strings.Contains(err.Error(), "write logs") {
		t.Fatalf("log writer error = %v", err)
	}
}

func TestAgentDeadlineAndCloseLifecycle(t *testing.T) {
	host, guest := net.Pipe()
	agent, err := NewAgent(host, testKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := agent.Signal(ctx, "TERM"); err == nil {
		t.Fatal("deadline did not interrupt blocked exchange")
	}
	_ = guest.Close()
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := agent.Close(); err != nil {
		t.Fatal("second close is not idempotent:", err)
	}
	if err := agent.Shutdown(context.Background()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("closed agent error = %v", err)
	}
}

func TestNewAgentValidation(t *testing.T) {
	if _, err := NewAgent(nil, testKey); err == nil {
		t.Fatal("nil connection accepted")
	}
	host, guest := net.Pipe()
	defer host.Close()
	defer guest.Close()
	if _, err := NewAgent(host, []byte("weak")); err == nil {
		t.Fatal("weak key accepted")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

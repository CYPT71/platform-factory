package guesttransport

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPartialExchangesRequireANewConnection(t *testing.T) {
	for _, phase := range []string{"request-header", "request-frame", "response-header", "response-frame"} {
		t.Run(phase, func(t *testing.T) {
			host, guest := net.Pipe()
			defer guest.Close()
			agent, err := NewAgent(host, testKey)
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			peerDone := make(chan error, 1)
			go func() {
				var err error
				defer func() { cancel(); peerDone <- err }()
				switch phase {
				case "request-header":
					_, err = io.ReadFull(guest, make([]byte, 2))
				case "request-frame":
					_, err = io.ReadFull(guest, make([]byte, 4))
					if err == nil {
						_, err = io.ReadFull(guest, make([]byte, 2))
					}
				default:
					codec, _ := NewCodec(guest, guest, testKey)
					var request Request
					if _, err = codec.Read(&request); err != nil {
						return
					}
					if phase == "response-header" {
						_, err = guest.Write([]byte{0, 0})
						return
					}
					var header [4]byte
					binary.BigEndian.PutUint32(header[:], 2)
					if _, err = guest.Write(header[:]); err == nil {
						_, err = guest.Write([]byte{'{'})
					}
				}
			}()
			if _, err := agent.State(ctx); !errors.Is(err, ErrReconnectRequired) {
				t.Fatalf("interrupted %s: %v", phase, err)
			}
			if err := <-peerDone; err != nil {
				t.Fatal(err)
			}
			if _, err := agent.State(context.Background()); !errors.Is(err, ErrReconnectRequired) {
				t.Fatalf("reused interrupted stream: %v", err)
			}
			// Reconnection means a fresh authenticated byte stream, not resetting the
			// sequence counter on the damaged one.
			newHost, newGuest := net.Pipe()
			defer newGuest.Close()
			fresh, err := NewAgent(newHost, testKey)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			done := make(chan error, 1)
			go func() {
				codec, _ := NewCodec(newGuest, newGuest, testKey)
				done <- ServeOne(context.Background(), codec, handlerFunc(func(context.Context, Request) Response { return Response{State: "running"} }))
			}()
			recovered, recoverCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer recoverCancel()
			if state, err := fresh.State(recovered); err != nil || state != "running" {
				t.Fatalf("fresh connection: %s %v", state, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthenticatedApplicationErrorPreservesConnection(t *testing.T) {
	host, guest := net.Pipe()
	defer guest.Close()
	agent, err := NewAgent(host, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	done := make(chan error, 1)
	go func() {
		codec, _ := NewCodec(guest, guest, testKey)
		for _, response := range []Response{{Error: "operation refused"}, {State: "running"}} {
			if err := ServeOne(context.Background(), codec, handlerFunc(func(context.Context, Request) Response { return response })); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := agent.Signal(ctx, "TERM"); err == nil || errors.Is(err, ErrReconnectRequired) {
		t.Fatalf("application error: %v", err)
	}
	if state, err := agent.State(ctx); err != nil || state != "running" {
		t.Fatalf("connection lost after application error: %s %v", state, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestQueuedCancellationDoesNotCloseActiveExchange(t *testing.T) {
	host, guest := net.Pipe()
	defer guest.Close()
	agent, err := NewAgent(host, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	requestSeen, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		codec, _ := NewCodec(guest, guest, testKey)
		done <- ServeOne(context.Background(), codec, handlerFunc(func(context.Context, Request) Response {
			close(requestSeen)
			<-release
			return Response{State: "running"}
		}))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := agent.State(ctx); first <- err }()
	select {
	case <-requestSeen:
	case <-ctx.Done():
		t.Fatal("first request did not reach peer")
	}
	queued, abort := context.WithCancel(ctx)
	abort()
	if _, err := agent.State(queued); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation: %v", err)
	}
	close(release)
	released = true
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// realUnixSocketPair dials a genuine OS-backed unix domain socket pair.
// Unlike net.Pipe's synchronous in-process rendezvous (every Read/Write pair
// unblocks in lockstep), each side here owns a real kernel socket buffer and
// a real half-close on Close - the same properties the production
// virtio-serial transport has that net.Pipe never exercises.
func realUnixSocketPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	// t.TempDir() nests under a path already close to the platform's
	// sockaddr_un limit (104 bytes on Darwin/BSD, 108 on Linux); a direct,
	// short name under the system temp root keeps every run under that
	// limit regardless of how deep the test's own temp dir is.
	addr := filepath.Join(os.TempDir(), fmt.Sprintf("gt-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	listener, err := net.Listen("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(addr)
	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()
	client, err := net.Dial("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case server := <-accepted:
		return client, server
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("accept timed out")
	}
	return nil, nil
}

// TestRecoveryHoldsOverARealOSBackedTransport proves the same reconnect
// contract exercised above against net.Pipe also holds over a genuine
// kernel-buffered stream: a severed connection mid-exchange must be rejected
// for reuse, and recovery means dialing a fresh socket, not resurrecting the
// torn one.
func TestRecoveryHoldsOverARealOSBackedTransport(t *testing.T) {
	host, guest := realUnixSocketPair(t)
	defer guest.Close()
	defer host.Close()
	agent, err := NewAgent(host, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	done := make(chan error, 1)
	go func() {
		codec, _ := NewCodec(guest, guest, testKey)
		done <- ServeOne(context.Background(), codec, handlerFunc(func(context.Context, Request) Response {
			return Response{State: "running"}
		}))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if state, err := agent.State(ctx); err != nil || state != "running" {
		t.Fatalf("real transport round trip: %s %v", state, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// Sever the connection with a genuine OS-level half-close after the
	// guest consumes the request but before it answers - the same shape of
	// failure a crashing or rebooting guest produces on real hardware.
	severed := make(chan error, 1)
	go func() {
		codec, err := NewCodec(guest, guest, testKey)
		if err != nil {
			severed <- err
			return
		}
		var request Request
		if _, err := codec.Read(&request); err != nil {
			severed <- err
			return
		}
		severed <- guest.Close()
	}()
	brokenCtx, brokenCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer brokenCancel()
	if _, err := agent.State(brokenCtx); !errors.Is(err, ErrReconnectRequired) {
		t.Fatalf("severed real transport: %v", err)
	}
	if err := <-severed; err != nil {
		t.Fatal(err)
	}
	if _, err := agent.State(context.Background()); !errors.Is(err, ErrReconnectRequired) {
		t.Fatalf("reused severed real connection: %v", err)
	}

	freshHost, freshGuest := realUnixSocketPair(t)
	defer freshGuest.Close()
	defer freshHost.Close()
	fresh, err := NewAgent(freshHost, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	freshDone := make(chan error, 1)
	go func() {
		codec, _ := NewCodec(freshGuest, freshGuest, testKey)
		freshDone <- ServeOne(context.Background(), codec, handlerFunc(func(context.Context, Request) Response {
			return Response{State: "running"}
		}))
	}()
	recovered, recoverCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer recoverCancel()
	if state, err := fresh.State(recovered); err != nil || state != "running" {
		t.Fatalf("fresh real connection: %s %v", state, err)
	}
	if err := <-freshDone; err != nil {
		t.Fatal(err)
	}
}

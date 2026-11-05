package guesttransport

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
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

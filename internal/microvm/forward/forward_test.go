package forward

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func freeListener(t *testing.T) *net.TCPListener {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func TestRelayEchoesBothDirections(t *testing.T) {
	guestListener := freeListener(t)
	defer guestListener.Close()
	guestAddr := guestListener.Addr().String()

	// Loops rather than accepting once: Relay's own readiness probe below
	// (waitListening) is itself a real client of the relay, so it also
	// gets forwarded to this guest listener and must not consume the one
	// connection the test actually cares about.
	go func() {
		for {
			conn, err := guestListener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 5)
				if _, err := io.ReadFull(conn, buf); err != nil {
					return
				}
				_, _ = conn.Write(bytes.ToUpper(buf))
			}()
		}
	}()

	hostListener := freeListener(t)
	hostAddr := hostListener.Addr().String()
	hostListener.Close() // Relay does its own Listen; just wanted a free port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relayDone := make(chan error, 1)
	go func() { relayDone <- Relay(ctx, hostAddr, guestAddr) }()
	waitListening(t, hostAddr)

	conn, err := net.Dial("tcp", hostAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "HELLO" {
		t.Fatalf("got %q, want HELLO", got)
	}

	cancel()
	select {
	case err := <-relayDone:
		if err != nil {
			t.Fatalf("Relay returned %v after cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Relay did not return after context cancellation")
	}
}

func TestRelayHTTPRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	guestAddr := server.Listener.Addr().String()

	hostListener := freeListener(t)
	hostAddr := hostListener.Addr().String()
	hostListener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Relay(ctx, hostAddr, guestAddr) }()
	waitListening(t, hostAddr)

	resp, err := http.Get("http://" + hostAddr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body=%q, want ok", body)
	}
}

func TestRelayDialFailureClosesConnectionWithoutCrashing(t *testing.T) {
	unreachable := freeListener(t)
	guestAddr := unreachable.Addr().String()
	unreachable.Close() // now nothing listens there

	hostListener := freeListener(t)
	hostAddr := hostListener.Addr().String()
	hostListener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Relay(ctx, hostAddr, guestAddr) }()
	waitListening(t, hostAddr)

	conn, err := net.Dial("tcp", hostAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected the relay to close the connection after a failed guest dial")
	}
}

func TestRelayReturnsErrorOnListenFailure(t *testing.T) {
	busy := freeListener(t)
	defer busy.Close()
	if err := Relay(context.Background(), busy.Addr().String(), "127.0.0.1:0"); err == nil {
		t.Fatal("expected an error binding an already-bound address")
	}
}

func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("relay never started listening on %s", addr)
}

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// partitionableTransport wraps a real http.RoundTripper and, while
// partitioned is set, fails every request the way a severed network path
// actually fails a client - no response, no partial data, just an error -
// rather than a fabricated non-2xx status a live server would never
// really send for "the network is gone".
type partitionableTransport struct {
	inner       http.RoundTripper
	partitioned atomic.Bool
}

func (t *partitionableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.partitioned.Load() {
		return nil, errors.New("dial tcp: connect: network is unreachable (simulated partition)")
	}
	return t.inner.RoundTrip(req)
}

// TestClientFailsClosedDuringPartitionThenRecovers proves the worker
// client's behavior across the fourth classic network fault - partition -
// which, unlike replay/duplication/corruption, is a property of the
// client's own resilience rather than the server's input validation: does
// it fail cleanly (return an error, no panic, no hang) while the network
// is gone, and resume normal operation the moment it's back, without
// needing to be restarted or re-registered.
func TestClientFailsClosedDuringPartitionThenRecovers(t *testing.T) {
	fake := newFakeControlPlane()
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	transport := &partitionableTransport{inner: server.Client().Transport}
	httpClient := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	client := &Client{
		HTTP: httpClient, BaseURL: server.URL,
		RequestTimeout: 500 * time.Millisecond,
		Execute: func(ctx context.Context, lease Lease) (string, error) {
			return "processed: " + lease.Payload, nil
		},
	}
	ctx := context.Background()

	if err := client.Register(ctx, "linux/amd64"); err != nil {
		t.Fatalf("register before partition: %v", err)
	}

	// Sever the network. Every subsequent call must return a clean error
	// within its own timeout - not hang past it, not panic.
	transport.partitioned.Store(true)

	heartbeatDone := make(chan error, 1)
	go func() { heartbeatDone <- client.Heartbeat(ctx) }()
	select {
	case err := <-heartbeatDone:
		if err == nil {
			t.Fatal("heartbeat succeeded during a simulated partition")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("heartbeat did not return during partition - looks hung, not failed closed")
	}

	pollDone := make(chan struct {
		ok  bool
		err error
	}, 1)
	go func() {
		ok, err := client.PollOnce(ctx)
		pollDone <- struct {
			ok  bool
			err error
		}{ok, err}
	}()
	select {
	case result := <-pollDone:
		if result.err == nil {
			t.Fatalf("PollOnce succeeded during a simulated partition: ok=%v", result.ok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PollOnce did not return during partition - looks hung, not failed closed")
	}

	// Heal the partition. The same client, unmodified, must work again -
	// no reconnect dance, no state left corrupted by the failed attempts.
	transport.partitioned.Store(false)

	if err := client.Heartbeat(ctx); err != nil {
		t.Fatalf("heartbeat after partition healed: %v", err)
	}
	ok, err := client.PollOnce(ctx)
	if err != nil || !ok {
		t.Fatalf("PollOnce after partition healed: ok=%v err=%v", ok, err)
	}
	select {
	case result := <-fake.completed:
		if result != "processed: do it" {
			t.Fatalf("result=%q", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion never reported after partition healed")
	}
}

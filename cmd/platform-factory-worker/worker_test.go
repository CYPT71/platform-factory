package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	typederrors "github.com/CYPT71/secure-oci-base/internal/errors"
	"github.com/CYPT71/secure-oci-base/internal/provenance"
)

// fakeControlPlane is a minimal stand-in for cmd/platform-factory-control-plane
// used to test Client in isolation, over a real HTTP connection
// (httptest.Server), without depending on the control-plane binary's own
// package (avoiding a cmd/-to-cmd/ import, which this project's plugin/
// binary boundary convention treats as a smell).
type fakeControlPlane struct {
	registered  atomic.Int64
	heartbeats  atomic.Int64
	leaseServed atomic.Bool
	completed   chan string
}

func newFakeControlPlane() *fakeControlPlane {
	return &fakeControlPlane{completed: make(chan string, 1)}
}

func (f *fakeControlPlane) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		f.registered.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /heartbeat", func(w http.ResponseWriter, r *http.Request) {
		f.heartbeats.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /lease/next", func(w http.ResponseWriter, r *http.Request) {
		if f.leaseServed.Swap(true) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// A real control plane's /lease/next response always carries every
		// Lease field (see internal/control.Lease's marshaling), and
		// Client.post's DisallowUnknownFields decoder requires the
		// worker's Lease type to be a complete mirror. A fixture sending
		// only {ID, Payload} - what this fake used to send - passes
		// silently while a real deployment fails every single poll; this
		// full fixture is what actually caught that on re-verification.
		_ = json.NewEncoder(w).Encode(Lease{
			ID: "lease-1", Payload: "do it", State: "assigned", Worker: "worker-under-test", Attempt: 1,
		})
	})
	mux.HandleFunc("POST /lease/complete", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.completed <- req["result"]
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /lease/status", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Lease{ID: "lease-1", State: "assigned", Worker: "worker-under-test", Attempt: 1})
	})
	return mux
}

func TestClientRegisterAndPollLifecycle(t *testing.T) {
	fake := newFakeControlPlane()
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	client := &Client{
		HTTP: server.Client(), BaseURL: server.URL,
		Execute: func(ctx context.Context, lease Lease) (string, error) {
			return "processed: " + lease.Payload, nil
		},
	}
	ctx := context.Background()
	if err := client.Register(ctx, "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if fake.registered.Load() != 1 {
		t.Fatalf("registered=%d", fake.registered.Load())
	}

	ok, err := client.PollOnce(ctx)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	select {
	case result := <-fake.completed:
		if result != "processed: do it" {
			t.Fatalf("result=%q", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion never reported")
	}

	// The fake serves exactly one lease; the second poll must report "no
	// work", not an error.
	ok, err = client.PollOnce(ctx)
	if err != nil || ok {
		t.Fatalf("second poll: ok=%v err=%v", ok, err)
	}
}

func TestClientSignsCompletionWhenSignerIsConfigured(t *testing.T) {
	identity, privateKey, err := provenance.GenerateWorkloadIdentity("worker-under-test")
	if err != nil {
		t.Fatal(err)
	}
	signer, err := provenance.NewWorkloadSigner(identity, privateKey)
	if err != nil {
		t.Fatal(err)
	}

	var registeredPublicKey string
	var completion completeRequest
	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var req Registration
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode register request: %v", err)
		}
		registeredPublicKey = req.PublicKey
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /lease/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Lease{ID: "lease-1", Payload: "do it", State: "assigned", Worker: "worker-under-test", Attempt: 1})
	})
	mux.HandleFunc("POST /lease/complete", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&completion); err != nil {
			t.Errorf("decode complete request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := &Client{
		HTTP: server.Client(), BaseURL: server.URL, Signer: signer, WorkerID: "worker-under-test",
		Execute: func(ctx context.Context, lease Lease) (string, error) { return "ok", nil },
	}
	ctx := context.Background()
	if err := client.Register(ctx, "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if registeredPublicKey != identity.PublicKey {
		t.Fatalf("registered public key=%q, want %q", registeredPublicKey, identity.PublicKey)
	}

	if ok, err := client.PollOnce(ctx); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if completion.Provenance == nil {
		t.Fatal("expected a signed provenance record on the completion request")
	}
	if completion.Provenance.WorkerID != "worker-under-test" || completion.Provenance.BuildID != "lease-1" {
		t.Fatalf("provenance=%+v", completion.Provenance)
	}
	if err := provenance.Verify(completion.Provenance, identity.PublicKey); err != nil {
		t.Fatalf("signature did not verify: %v", err)
	}
}

func TestClientPollOnceReportsExecutionFailureWithoutCompleting(t *testing.T) {
	fake := newFakeControlPlane()
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	client := &Client{
		HTTP: server.Client(), BaseURL: server.URL,
		Execute: func(ctx context.Context, lease Lease) (string, error) {
			return "", errors.New("boom")
		},
	}
	ok, err := client.PollOnce(context.Background())
	if err == nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	select {
	case result := <-fake.completed:
		t.Fatalf("completion reported despite execution failure: %q", result)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestClientRunSendsHeartbeatsAndPolls(t *testing.T) {
	fake := newFakeControlPlane()
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	client := &Client{
		HTTP: server.Client(), BaseURL: server.URL,
		Execute: func(ctx context.Context, lease Lease) (string, error) { return "done", nil },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := client.Run(ctx, 20*time.Millisecond, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	if fake.heartbeats.Load() == 0 {
		t.Fatal("no heartbeats were sent")
	}
	select {
	case <-fake.completed:
	default:
		t.Fatal("no lease was ever polled and completed during Run")
	}
}

func TestHeartbeatsContinueDuringLongLease(t *testing.T) {
	fake := newFakeControlPlane()
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	started := make(chan struct{})
	client := &Client{
		HTTP: server.Client(), BaseURL: server.URL,
		Execute: func(ctx context.Context, lease Lease) (string, error) {
			close(started)
			select {
			case <-time.After(180 * time.Millisecond):
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 260*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, 15*time.Millisecond, 5*time.Millisecond) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("lease did not start")
	}
	time.Sleep(100 * time.Millisecond)
	if got := fake.heartbeats.Load(); got < 3 {
		t.Fatalf("heartbeats stalled during execution: got %d", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunExecutesLeasesUpToAdvertisedParallelism(t *testing.T) {
	var served atomic.Int64
	var active atomic.Int64
	var maximum atomic.Int64
	var completed atomic.Int64
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /heartbeat", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /lease/next", func(w http.ResponseWriter, _ *http.Request) {
		number := served.Add(1)
		if number > 2 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(Lease{ID: fmt.Sprintf("lease-%d", number), Payload: "work", State: "assigned", Attempt: 1})
	})
	mux.HandleFunc("POST /lease/complete", func(w http.ResponseWriter, _ *http.Request) {
		completed.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /lease/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Lease{ID: r.URL.Query().Get("id"), State: "assigned", Attempt: 1})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := &Client{HTTP: server.Client(), BaseURL: server.URL, MaxParallel: 2}
	client.Execute = func(ctx context.Context, _ Lease) (string, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
		}
		started <- struct{}{}
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, 20*time.Millisecond, time.Millisecond) }()
	for range 2 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("two leases did not start concurrently")
		}
	}
	close(release)
	for completed.Load() < 2 && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != 2 || completed.Load() != 2 {
		t.Fatalf("maximum=%d completed=%d", maximum.Load(), completed.Load())
	}
}

func TestPollOncePropagatesRemoteCancellationToExecution(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var completed atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /lease/next", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Lease{ID: "lease-1", Payload: "work", State: "assigned", Attempt: 1})
	})
	mux.HandleFunc("GET /lease/status", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Lease{ID: "lease-1", State: "canceled", CanceledBy: "operator"})
	})
	mux.HandleFunc("POST /lease/complete", func(w http.ResponseWriter, _ *http.Request) {
		completed.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := &Client{HTTP: server.Client(), BaseURL: server.URL, CancellationPollInterval: time.Millisecond}
	client.Execute = func(ctx context.Context, _ Lease) (string, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	}
	ok, err := client.PollOnce(context.Background())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	select {
	case <-started:
	default:
		t.Fatal("execution did not start")
	}
	select {
	case <-canceled:
	default:
		t.Fatal("execution context was not canceled")
	}
	if completed.Load() {
		t.Fatal("canceled lease was reported as completed")
	}
}

func TestLeaseStatusRejectsInvalidResponses(t *testing.T) {
	tests := map[string]struct {
		handler http.HandlerFunc
		want    string
	}{
		"status": {
			handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "missing", http.StatusNotFound) },
			want:    "unexpected status 404",
		},
		"oversized": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(make([]byte, maxResponseBodyBytes+1))
			},
			want: "response exceeds 1 MiB",
		},
		"unknown field": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"id":"lease-1","extra":true}`)
			},
			want: "unknown field",
		},
		"trailing JSON": {
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"id":"lease-1"} {}`) },
			want:    "exactly one JSON value",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			client := &Client{HTTP: server.Client(), BaseURL: server.URL}
			if _, err := client.leaseStatus(context.Background(), "lease-1"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestPollOnceStopsExecutionWhenCancellationStatusFails(t *testing.T) {
	canceled := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /lease/next", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Lease{ID: "lease-1", State: "assigned"})
	})
	mux.HandleFunc("GET /lease/status", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := &Client{HTTP: server.Client(), BaseURL: server.URL, CancellationPollInterval: time.Millisecond}
	client.Execute = func(ctx context.Context, _ Lease) (string, error) {
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	}
	ok, err := client.PollOnce(context.Background())
	if !ok || err == nil || !strings.Contains(err.Error(), "poll cancellation") {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("executor context was not canceled after status failure")
	}
}

func TestClientRejectsInvalidRuntimeParameters(t *testing.T) {
	client := &Client{}
	if err := client.Run(context.Background(), 0, time.Second); err == nil {
		t.Fatal("zero heartbeat interval accepted")
	} else if !typederrors.HasCode(err, typederrors.CodeInvalidArgument) {
		t.Fatalf("Run: got code %q, want %q (err=%v)", typederrors.GetErrorCode(err), typederrors.CodeInvalidArgument, err)
	}
	if err := client.Register(context.Background(), " "); err == nil {
		t.Fatal("empty platform accepted")
	} else if !typederrors.HasCode(err, typederrors.CodeInvalidArgument) {
		t.Fatalf("Register: got code %q, want %q (err=%v)", typederrors.GetErrorCode(err), typederrors.CodeInvalidArgument, err)
	}
	if _, err := client.PollOnce(context.Background()); err == nil {
		t.Fatal("nil executor accepted")
	} else if !typederrors.HasCode(err, typederrors.CodeInvalidArgument) {
		t.Fatalf("PollOnce: got code %q, want %q (err=%v)", typederrors.GetErrorCode(err), typederrors.CodeInvalidArgument, err)
	}
}

func TestDefaultPlatformIsNonEmpty(t *testing.T) {
	if defaultPlatform() == "" || defaultPlatform() == "/" {
		t.Fatalf("defaultPlatform()=%q", defaultPlatform())
	}
}

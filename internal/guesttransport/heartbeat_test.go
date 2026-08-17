package guesttransport

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// serveState answers exactly n OpState requests with "running", one at a
// time, blocking between them until told to serve the next - giving the
// test precise control over when the guest "goes quiet" and when it
// resumes, instead of racing real wall-clock timing against the guest
// loop's own pace.
func serveStateOnDemand(t *testing.T, codec *Codec, releases <-chan struct{}) {
	t.Helper()
	go func() {
		for range releases {
			if err := ServeOne(context.Background(), codec, handlerFunc(func(context.Context, Request) Response {
				return Response{State: "running"}
			})); err != nil {
				return
			}
		}
	}()
}

func TestRunHeartbeatDetectsAndRecoversFromStuckGuest(t *testing.T) {
	host, guest := net.Pipe()
	agent, err := NewAgent(host, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	guestCodec, err := NewCodec(guest, guest, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()

	// Never closed: a background sender may still be selecting on it when
	// the test returns (racing a close would be exactly the kind of
	// send-on-closed-channel hazard this is avoiding), and every
	// goroutine holding it exits via ctx.Done() on its own once cancel()
	// runs below.
	releases := make(chan struct{})
	serveStateOnDemand(t, guestCodec, releases)

	var mu sync.Mutex
	var events []struct {
		stuck  bool
		missed int
	}
	onStatusChange := func(stuck bool, consecutiveMisses int) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, struct {
			stuck  bool
			missed int
		}{stuck, consecutiveMisses})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const interval = 20 * time.Millisecond
	const threshold = 3
	go RunHeartbeat(ctx, agent, interval, threshold, onStatusChange)

	// Two responsive probes: the guest answers immediately each time -
	// never crosses the stuck threshold, so no event fires yet.
	releases <- struct{}{}
	releases <- struct{}{}
	time.Sleep(3 * interval)
	mu.Lock()
	gotBeforeStuck := len(events)
	mu.Unlock()
	if gotBeforeStuck != 0 {
		t.Fatalf("events fired before any miss: %d", gotBeforeStuck)
	}

	// Now the guest goes quiet: no releases sent, so every probe times
	// out. After `threshold` consecutive misses, exactly one "stuck"
	// event must fire - not one per miss.
	time.Sleep(time.Duration(threshold+2) * interval)
	mu.Lock()
	if len(events) != 1 || !events[0].stuck || events[0].missed != threshold {
		t.Fatalf("events after going quiet = %+v, want exactly one stuck event at threshold %d", events, threshold)
	}
	mu.Unlock()

	// Still quiet for a while longer: still exactly one event (no
	// re-firing on every subsequent miss once already stuck).
	time.Sleep(3 * interval)
	mu.Lock()
	stillOne := len(events)
	mu.Unlock()
	if stillOne != 1 {
		t.Fatalf("events fired again while still stuck: %d", stillOne)
	}

	// The guest recovers: answer every subsequent probe immediately.
	go func() {
		for {
			select {
			case releases <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}()
	// Poll instead of a single fixed sleep: a loaded CI runner can easily
	// miss a 3-tick (60ms) window between starting the recovery goroutine
	// and its first successful probe landing, with nothing wrong in
	// RunHeartbeat itself - wait for the second event however long the
	// scheduler actually takes, capped well above any real recovery
	// latency, rather than flaking on scheduling jitter.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(interval)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[1].stuck {
		t.Fatalf("events after recovery = %+v, want a second, non-stuck event", events)
	}
}

func TestRunHeartbeatStopsOnContextCancellation(t *testing.T) {
	host, guest := net.Pipe()
	agent, err := NewAgent(host, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	defer guest.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunHeartbeat(ctx, agent, 10*time.Millisecond, 2, nil)
		close(done)
	}()
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunHeartbeat did not return after context cancellation")
	}
}

func TestRunHeartbeatRejectsInvalidParameters(t *testing.T) {
	host, guest := net.Pipe()
	agent, err := NewAgent(host, testKey)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	defer guest.Close()

	for _, test := range []struct {
		interval  time.Duration
		threshold int
	}{
		{0, 3},
		{10 * time.Millisecond, 0},
		{-time.Second, 3},
	} {
		done := make(chan struct{})
		go func() {
			RunHeartbeat(context.Background(), agent, test.interval, test.threshold, nil)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("RunHeartbeat(interval=%v, threshold=%d) did not return immediately", test.interval, test.threshold)
		}
	}
}

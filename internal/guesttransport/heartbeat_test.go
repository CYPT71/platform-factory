package guesttransport

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestRunHeartbeatDetectsAndRecoversFromStuckGuest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	results := make(chan error)
	type event struct {
		stuck  bool
		missed int
	}
	events := make(chan event, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(ctx, func(ctx context.Context) (string, error) {
			select {
			case err := <-results:
				return "running", err
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}, ticks, time.Minute, 3, func(stuck bool, missed int) {
			events <- event{stuck, missed}
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("heartbeat did not stop after cancellation")
		}
	})
	sendTick := func() {
		t.Helper()
		select {
		case ticks <- time.Time{}:
		case <-time.After(5 * time.Second):
			t.Fatal("heartbeat did not accept a probe tick")
		}
	}
	// Inject probe results instead of racing 20ms socket deadlines. Transport
	// framing is covered by the agent and protocol tests; this checks status
	// transitions, including suppression of repeated notifications.
	sendTick()
	miss := errors.New("guest unavailable")
	for i, result := range []error{nil, nil, miss, miss, miss, miss, miss, miss, nil, nil} {
		select {
		case results <- result:
		case <-time.After(5 * time.Second):
			t.Fatal("heartbeat did not request a probe result")
		}
		// Accepting the next tick proves the previous callback has completed.
		sendTick()
		switch i {
		case 4, 8:
			want := event{stuck: i == 4}
			if want.stuck {
				want.missed = 3
			}
			select {
			case got := <-events:
				if got != want {
					t.Fatalf("probe %d: event = %+v, want %+v", i, got, want)
				}
			default:
				t.Fatalf("probe %d: missing status transition", i)
			}
		default:
			select {
			case got := <-events:
				t.Fatalf("probe %d: unexpected event %+v", i, got)
			default:
			}
		}
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

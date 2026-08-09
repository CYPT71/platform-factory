package directboot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/guesttransport"
	api "github.com/CYPT71/secure-oci-base/internal/microvm"
)

type guestHandlerFunc func(context.Context, guesttransport.Request) guesttransport.Response

func (f guestHandlerFunc) Handle(ctx context.Context, request guesttransport.Request) guesttransport.Response {
	return f(ctx, request)
}

func TestPrepareGuestAgentProducesAuthenticatedUsablePair(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	var agent api.GuestAgent
	hostAgent, guest, err := prepareGuestAgent(context.Background(), GuestAgentOptions{
		SessionKey: key,
		OnReady: func(ready api.GuestAgent) error {
			agent = ready
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close()
	if closer, ok := hostAgent.(io.Closer); ok {
		defer closer.Close()
	}
	if agent == nil || agent != hostAgent {
		t.Fatal("OnReady did not receive the retained host agent")
	}
	codec, err := guesttransport.NewCodec(guest, guest, key)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- guesttransport.ServeOne(context.Background(), codec,
			guestHandlerFunc(func(_ context.Context, request guesttransport.Request) guesttransport.Response {
				if request.Operation != guesttransport.OpSignal || request.Signal != "TERM" {
					t.Errorf("request=%+v", request)
				}
				return guesttransport.Response{}
			}))
	}()
	if err := agent.Signal(context.Background(), "TERM"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPrepareGuestAgentFailsClosed(t *testing.T) {
	if _, _, err := prepareGuestAgent(context.Background(), GuestAgentOptions{
		SessionKey: []byte("weak"), OnReady: func(api.GuestAgent) error { return nil },
	}); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("weak key error=%v", err)
	}
	if _, _, err := prepareGuestAgent(context.Background(), GuestAgentOptions{
		SessionKey: bytes.Repeat([]byte{1}, 32),
	}); err == nil || !strings.Contains(err.Error(), "OnReady") {
		t.Fatalf("callback error=%v", err)
	}
	sentinel := errors.New("consumer rejected agent")
	if _, _, err := prepareGuestAgent(context.Background(), GuestAgentOptions{
		SessionKey: bytes.Repeat([]byte{1}, 32),
		OnReady:    func(api.GuestAgent) error { return sentinel },
	}); !errors.Is(err, sentinel) {
		t.Fatalf("consumer error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := prepareGuestAgent(ctx, GuestAgentOptions{
		SessionKey: bytes.Repeat([]byte{1}, 32),
		OnReady:    func(api.GuestAgent) error { return nil },
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error=%v", err)
	}
}

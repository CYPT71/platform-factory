package guest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

func TestOpenGuestAgentFailsClosedAndOwnsFailedConnection(t *testing.T) {
	if _, err := OpenAgent(context.Background(), "vm", nil); err == nil {
		t.Fatal("missing connector accepted")
	}
	if _, err := OpenAgent(context.Background(), "vm", func(context.Context, string) (io.ReadWriteCloser, []byte, error) {
		return nil, nil, errors.New("dial failed")
	}); err == nil || !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("connector error = %v", err)
	}

	host, guest := net.Pipe()
	defer guest.Close()
	if _, err := OpenAgent(context.Background(), "vm", func(_ context.Context, id string) (io.ReadWriteCloser, []byte, error) {
		if id != "vm" {
			t.Fatalf("machine id = %q", id)
		}
		return host, []byte("weak"), nil
	}); err == nil {
		t.Fatal("weak session key accepted")
	}
	if _, err := host.Write([]byte{1}); err == nil {
		t.Fatal("connection was not closed after adapter failure")
	}
}

func TestOpenGuestAgentReturnsPublicContract(t *testing.T) {
	host, guest := net.Pipe()
	defer guest.Close()
	key := bytes.Repeat([]byte{1}, 32)
	agent, err := OpenAgent(context.Background(), "vm", func(context.Context, string) (io.ReadWriteCloser, []byte, error) {
		return host, key, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := agent.(io.Closer); !ok {
		t.Fatal("agent does not expose lifecycle close")
	} else if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
}

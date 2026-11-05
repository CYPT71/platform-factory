package guesttransport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	api "github.com/CYPT71/platform-factory/internal/microvm"
)

// MaxIOBytes leaves sufficient room in a frame for the authenticated envelope,
// arguments, and response metadata.
const MaxIOBytes = 768 << 10

type deadlineConn interface {
	SetDeadline(time.Time) error
}

// Agent adapts an authenticated transport connection to the internal GuestAgent
// port. The caller supplies the connection: a backend may use virtio-vsock
// when available, while unit tests and early integrations can use a socket.
type Agent struct {
	conn   io.ReadWriteCloser
	client *Client

	requestGate chan struct{}
	stateMu     sync.Mutex
	closed      bool
	failure     error
}

var _ api.GuestAgent = (*Agent)(nil)

func NewAgent(conn io.ReadWriteCloser, sessionKey []byte) (*Agent, error) {
	if conn == nil {
		return nil, errors.New("guest transport: connection is required")
	}
	codec, err := NewCodec(conn, conn, sessionKey)
	if err != nil {
		return nil, err
	}
	return &Agent{conn: conn, client: NewClient(codec), requestGate: make(chan struct{}, 1)}, nil
}

func (a *Agent) Exec(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if stdin == nil {
		stdin = emptyReader{}
	}
	input, err := io.ReadAll(io.LimitReader(stdin, MaxIOBytes+1))
	if err != nil {
		return 0, fmt.Errorf("guest transport: read stdin: %w", err)
	}
	if len(input) > MaxIOBytes {
		return 0, errors.New("guest transport: stdin exceeds 768 KiB")
	}
	response, err := a.do(ctx, Request{Operation: OpExec, Args: args, Stdin: input})
	if err != nil {
		return response.ExitCode, err
	}
	if stdout != nil {
		if _, err := stdout.Write(response.Stdout); err != nil {
			return response.ExitCode, fmt.Errorf("guest transport: write stdout: %w", err)
		}
	}
	if stderr != nil {
		if _, err := stderr.Write(response.Stderr); err != nil {
			return response.ExitCode, fmt.Errorf("guest transport: write stderr: %w", err)
		}
	}
	return response.ExitCode, nil
}

func (a *Agent) Signal(ctx context.Context, signal string) error {
	_, err := a.do(ctx, Request{Operation: OpSignal, Signal: signal})
	return err
}

func (a *Agent) Shutdown(ctx context.Context) error {
	_, err := a.do(ctx, Request{Operation: OpShutdown})
	return err
}

// State and Logs expose the remaining v4 control operations without widening
// the stable public GuestAgent interface prematurely.
func (a *Agent) State(ctx context.Context) (string, error) {
	response, err := a.do(ctx, Request{Operation: OpState})
	return response.State, err
}

func (a *Agent) Logs(ctx context.Context, dst io.Writer) error {
	response, err := a.do(ctx, Request{Operation: OpLogs})
	if err != nil {
		return err
	}
	if dst == nil {
		return nil
	}
	if _, err := dst.Write(response.Logs); err != nil {
		return fmt.Errorf("guest transport: write logs: %w", err)
	}
	return nil
}

func (a *Agent) do(ctx context.Context, request Request) (Response, error) {
	select {
	case a.requestGate <- struct{}{}:
		defer func() { <-a.requestGate }()
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	a.stateMu.Lock()
	if a.closed {
		failure := a.failure
		a.stateMu.Unlock()
		if failure != nil {
			return Response{}, fmt.Errorf("%w: %w", ErrReconnectRequired, failure)
		}
		return Response{}, errors.New("guest transport: agent is closed")
	}
	a.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if conn, ok := a.conn.(deadlineConn); ok {
		deadline, hasDeadline := ctx.Deadline()
		if hasDeadline {
			if err := conn.SetDeadline(deadline); err != nil {
				return Response{}, fmt.Errorf("guest transport: set deadline: %w", err)
			}
			defer conn.SetDeadline(time.Time{}) //nolint:errcheck // best-effort reset
		}
	}
	stop := context.AfterFunc(ctx, func() { a.invalidate(ctx.Err()) })
	defer stop()
	response, err := a.client.Do(ctx, request)
	var remote responseError
	if err != nil && !errors.As(err, &remote) {
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
		a.invalidate(err)
		return response, fmt.Errorf("%w: %w", ErrReconnectRequired, err)
	}
	return response, err
}

// ErrReconnectRequired marks an exchange whose byte/operation boundary is
// uncertain. Never replay it automatically, especially Exec or Shutdown.
var ErrReconnectRequired = errors.New("guest transport: agent is closed; reconnect with a new connection before retrying")

func (a *Agent) invalidate(err error) {
	a.stateMu.Lock()
	if a.failure == nil {
		a.failure = err
	}
	alreadyClosed := a.closed
	a.closed = true
	a.stateMu.Unlock()
	if !alreadyClosed {
		_ = a.conn.Close()
	}
}

func (a *Agent) Close() error {
	a.stateMu.Lock()
	if a.closed {
		a.stateMu.Unlock()
		return nil
	}
	a.closed = true
	a.stateMu.Unlock()
	return a.conn.Close()
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

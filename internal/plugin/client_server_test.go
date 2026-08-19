package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// pipeConn wires a api.Server directly to nothing but raw pipes, exercising the
// exact same WriteMessage/ReadMessage path a real subprocess would use,
// without paying for process startup in every test case.
type pipeConn struct {
	serverIn  *io.PipeReader
	serverOut *io.PipeWriter
	clientIn  *io.PipeReader
	clientOut *io.PipeWriter
}

func newPipeConn() *pipeConn {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	return &pipeConn{serverIn: serverIn, serverOut: serverOut, clientIn: clientIn, clientOut: clientOut}
}

// callOverPipe drives one request/response exchange directly through
// WriteMessage/ReadMessage against a api.Server running on the other end of an
// io.Pipe, without a Client (some tests need to send protocol-invalid input
// a real Client would refuse to construct).
type testHandler func(context.Context, json.RawMessage) (any, error)

type testServer struct {
	name, version string
	handlers      map[string]testHandler
}

func (s *testServer) Handle(method string, handler testHandler) { s.handlers[method] = handler }

func (s *testServer) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	reader := bufio.NewReader(r)
	for {
		raw, err := ReadMessage(reader)
		if err != nil {
			return err
		}
		var req Request
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
		resp := Response{ID: req.ID, TraceID: req.TraceID, OperationID: req.OperationID}
		var value any
		if req.Method == "v1.hello" {
			capabilities := make([]string, 0, len(s.handlers))
			for method := range s.handlers {
				capabilities = append(capabilities, method)
			}
			value = HelloResult{APIVersion: ProtocolVersion, Name: s.name, Version: s.version, Capabilities: capabilities}
		} else if handler := s.handlers[strings.TrimPrefix(req.Method, "v1.")]; handler != nil {
			value, err = handler(ctx, req.Params)
		} else {
			err = errors.New("unknown method")
		}
		if err != nil {
			resp.Error = &RPCError{Code: 500, Message: err.Error()}
		} else {
			resp.Result, err = json.Marshal(value)
			if err != nil {
				return err
			}
		}
		if err := WriteMessage(w, resp); err != nil {
			return err
		}
	}
}

func serveInBackground(t *testing.T, server *testServer, conn *pipeConn) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), conn.serverIn, conn.serverOut) }()
	t.Cleanup(func() {
		_ = conn.clientOut.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not shut down after client closed the connection")
		}
	})
}

func echoServer() *testServer {
	server := &testServer{name: "echo", version: "v0.0.1", handlers: map[string]testHandler{}}
	server.Handle("deployment.observe", func(_ context.Context, raw json.RawMessage) (any, error) {
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		return value, nil
	})
	server.Handle("migration.inspect", func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("deliberate failure")
	})
	return server
}

func TestClientServerHandshakeAndCall(t *testing.T) {
	conn := newPipeConn()
	serveInBackground(t, echoServer(), conn)

	client, err := attachClient(t, conn)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if hello := client.Hello(); hello.Name != "echo" || hello.APIVersion != ProtocolVersion {
		t.Fatalf("hello=%+v", hello)
	}
	if !client.HasCapability("deployment.observe") || client.HasCapability("nonexistent") {
		t.Fatalf("capabilities=%v", client.Hello().Capabilities)
	}

	var result map[string]any
	if err := client.Call(context.Background(), "v1.deployment.observe", map[string]string{"key": "value"}, &result); err != nil {
		t.Fatalf("call: %v", err)
	}
	if result["key"] != "value" {
		t.Fatalf("result=%v", result)
	}
}

func TestClientCallSurfacesHandlerError(t *testing.T) {
	conn := newPipeConn()
	serveInBackground(t, echoServer(), conn)
	client, err := attachClient(t, conn)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	err = client.Call(context.Background(), "v1.migration.inspect", nil, nil)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Message != "deliberate failure" {
		t.Fatalf("err=%v", err)
	}
}

func TestClientCallUnknownMethod(t *testing.T) {
	conn := newPipeConn()
	serveInBackground(t, echoServer(), conn)
	client, err := attachClient(t, conn)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	err = client.Call(context.Background(), "v1.future-mutation", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "CallWithIdempotency is required") {
		t.Fatalf("unsafe method error=%v", err)
	}
}

func TestHandshakeRejectsWrongAPIVersion(t *testing.T) {
	conn := newPipeConn()
	// Hand-craft a non-conformant handshake response directly (a real
	// api.Server always reports ProtocolVersion, so this exercises
	// Client.handshake's version check without needing a nonconforming
	// api.Server implementation).
	go func() {
		reader := bufio.NewReader(conn.serverIn)
		raw, err := ReadMessage(reader)
		if err != nil {
			return
		}
		var req Request
		_ = json.Unmarshal(raw, &req)
		result, _ := json.Marshal(HelloResult{APIVersion: "v0", Name: "legacy", Version: "v0.0.1"})
		_ = WriteMessage(conn.serverOut, Response{ID: req.ID, Result: result})
	}()

	c := &Client{stdin: conn.clientOut, reader: bufio.NewReader(conn.clientIn)}
	if err := c.handshake(context.Background()); err == nil {
		t.Fatal("expected handshake to reject a mismatched API version")
	}
	_ = conn.clientOut.Close()
}

func attachClient(t *testing.T, conn *pipeConn) (*Client, error) {
	t.Helper()
	c := &Client{stdin: conn.clientOut, reader: bufio.NewReader(conn.clientIn)}
	if err := c.handshake(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

func TestStartRejectsNonexistentExecutable(t *testing.T) {
	if _, err := Start(context.Background(), "/does/not/exist/secure-oci-plugin", nil, nil); err == nil {
		t.Fatal("expected an error for a nonexistent executable")
	}
}

func TestCallRejectsAlreadyCanceledContext(t *testing.T) {
	conn := newPipeConn()
	serveInBackground(t, echoServer(), conn)
	client, err := attachClient(t, conn)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Call(ctx, "v1.deployment.observe", nil, nil); err == nil {
		t.Fatal("expected an error for an already-canceled context")
	}
}

type failingWriteCloser struct{}

func (failingWriteCloser) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func (failingWriteCloser) Close() error              { return nil }

func TestCallSurfacesWriteFailure(t *testing.T) {
	conn := newPipeConn()
	c := &Client{stdin: failingWriteCloser{}, reader: bufio.NewReader(conn.clientIn)}
	if err := c.Call(context.Background(), "v1.deployment.observe", nil, nil); err == nil {
		t.Fatal("expected an error when writing the request fails")
	}
}

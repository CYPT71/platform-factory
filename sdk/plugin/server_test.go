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

// serveOverPipe runs a Server against in-process pipes and returns a
// helper that issues one framed request and reads the framed response,
// exercising NewServer, Handle, Serve and dispatch without a subprocess.
func serveOverPipe(t *testing.T, server *Server) (func(method string, params any) Response, func()) {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), serverIn, serverOut) }()
	reader := bufio.NewReader(clientIn)
	id := 0
	call := func(method string, params any) Response {
		id++
		var raw json.RawMessage
		if params != nil {
			data, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			raw = data
		}
		if err := WriteMessage(clientOut, Request{ID: itoa(id), Method: method, Params: raw}); err != nil {
			t.Fatal(err)
		}
		body, err := ReadMessage(reader)
		if err != nil {
			t.Fatal(err)
		}
		var response Response
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	cleanup := func() {
		_ = clientOut.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not shut down")
		}
	}
	return call, cleanup
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestServerHandshakeDispatchAndErrors(t *testing.T) {
	server := NewServer("fixture", "v1.2.3")
	server.Handle(CapabilityDetect, func(_ context.Context, params json.RawMessage) (any, error) {
		var request DetectParams
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		if request.Path == "" {
			return nil, errors.New("path required")
		}
		return DetectResult{Kind: "fixture", Profile: "static"}, nil
	})
	call, cleanup := serveOverPipe(t, server)
	defer cleanup()

	hello := call("v1.hello", nil)
	if hello.Error != nil {
		t.Fatalf("hello error: %+v", hello.Error)
	}
	var result HelloResult
	if err := json.Unmarshal(hello.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Name != "fixture" || result.APIVersion != ProtocolVersion ||
		len(result.Capabilities) != 1 || result.Capabilities[0] != CapabilityDetect {
		t.Fatalf("hello=%+v", result)
	}

	ok := call("v1.detect", DetectParams{Path: "/somewhere"})
	if ok.Error != nil {
		t.Fatalf("detect error: %+v", ok.Error)
	}
	var detected DetectResult
	if err := json.Unmarshal(ok.Result, &detected); err != nil || detected.Kind != "fixture" {
		t.Fatalf("detected=%+v err=%v", detected, err)
	}

	handlerErr := call("v1.detect", DetectParams{})
	if handlerErr.Error == nil || handlerErr.Error.Code != 500 {
		t.Fatalf("expected handler error 500, got %+v", handlerErr.Error)
	}

	unknown := call("v1.nonexistent", nil)
	if unknown.Error == nil || unknown.Error.Code != 404 {
		t.Fatalf("expected 404, got %+v", unknown.Error)
	}
}

func TestServerServeReturnsOnCanceledContext(t *testing.T) {
	server := NewServer("fixture", "v1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	if err := server.Serve(ctx, reader, io.Discard); err != nil {
		t.Fatalf("serve returned %v on a canceled context", err)
	}
}

func TestServerServeReportsProtocolDecodeAndWriteFailures(t *testing.T) {
	server := NewServer("fixture", "v1")
	server.Handle("bad-result", func(context.Context, json.RawMessage) (any, error) {
		return make(chan int), nil
	})
	for name, tc := range map[string]struct {
		input  string
		writer io.Writer
		want   string
	}{
		"protocol": {
			input:  "not-a-header\r\n\r\n",
			writer: io.Discard,
			want:   "plugin: serve",
		},
		"decode": {
			input:  framedRaw("{"),
			writer: io.Discard,
			want:   "decode request",
		},
		"write": {
			input:  framedRaw(`{"id":"1","method":"v1.hello"}`),
			writer: &failingWriter{failAt: 1},
			want:   "write response",
		},
		"result": {
			input:  framedRaw(`{"id":"1","method":"v1.bad-result"}`),
			writer: io.Discard,
			want:   "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := server.Serve(context.Background(), strings.NewReader(tc.input), tc.writer)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func framedRaw(body string) string {
	return "Content-Type: " + ContentType + "\r\nContent-Length: " +
		itoa(len(body)) + "\r\n\r\n" + body
}

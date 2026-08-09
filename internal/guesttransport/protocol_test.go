package guesttransport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
)

var testKey = bytes.Repeat([]byte{0x5a}, 32)

type handlerFunc func(context.Context, Request) Response

func (f handlerFunc) Handle(ctx context.Context, request Request) Response {
	return f(ctx, request)
}

func TestAuthenticatedRoundTripSupportsControlOperations(t *testing.T) {
	for _, request := range []Request{
		{Operation: OpExec, Args: []string{"/bin/echo", "ok"}, Stdin: []byte("input")},
		{Operation: OpSignal, Signal: "TERM"},
		{Operation: OpShutdown},
		{Operation: OpState},
		{Operation: OpLogs},
	} {
		t.Run(string(request.Operation), func(t *testing.T) {
			host, guest := net.Pipe()
			defer host.Close()
			defer guest.Close()
			hostCodec, _ := NewCodec(host, host, testKey)
			guestCodec, _ := NewCodec(guest, guest, testKey)
			serverError := make(chan error, 1)
			go func() {
				serverError <- ServeOne(context.Background(), guestCodec, handlerFunc(func(_ context.Context, got Request) Response {
					if got.Operation != request.Operation {
						t.Errorf("operation = %q, want %q", got.Operation, request.Operation)
					}
					return Response{ExitCode: 7, State: "running", Stdout: []byte("out"), Logs: []byte("log")}
				}))
			}()
			response, err := NewClient(hostCodec).Do(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if response.ExitCode != 7 || response.State != "running" || string(response.Logs) != "log" {
				t.Fatalf("response = %+v", response)
			}
			if err := <-serverError; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCodecRejectsTamperingReplayAndOversizedFrames(t *testing.T) {
	var wire bytes.Buffer
	writer, _ := NewCodec(bytes.NewReader(nil), &wire, testKey)
	if err := writer.Write(1, Request{Operation: OpState}); err != nil {
		t.Fatal(err)
	}
	var tampered envelope
	if err := json.Unmarshal(wire.Bytes()[4:], &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.MAC = "00" + tampered.MAC[2:]
	raw, _ := json.Marshal(tampered)
	var tamperedWire bytes.Buffer
	_ = binary.Write(&tamperedWire, binary.BigEndian, uint32(len(raw)))
	_, _ = tamperedWire.Write(raw)
	frame := tamperedWire.Bytes()
	reader, _ := NewCodec(bytes.NewReader(frame), bytes.NewBuffer(nil), testKey)
	var request Request
	if _, err := reader.Read(&request); err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("tamper error = %v", err)
	}

	wire.Reset()
	if err := writer.Write(2, Request{Operation: OpState}); err != nil {
		t.Fatal(err)
	}
	replayed := append(append([]byte(nil), wire.Bytes()...), wire.Bytes()...)
	reader, _ = NewCodec(bytes.NewReader(replayed), bytes.NewBuffer(nil), testKey)
	if _, err := reader.Read(&request); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(&request); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("replay error = %v", err)
	}

	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrameSize+1)
	reader, _ = NewCodec(bytes.NewReader(header[:]), bytes.NewBuffer(nil), testKey)
	if _, err := reader.Read(&request); err == nil || !strings.Contains(err.Error(), "frame size") {
		t.Fatalf("size error = %v", err)
	}
}

func TestCodecRejectsWeakKeysAndUnknownEnvelopeFields(t *testing.T) {
	if _, err := NewCodec(bytes.NewReader(nil), bytes.NewBuffer(nil), []byte("weak")); err == nil {
		t.Fatal("weak key accepted")
	}
	body, _ := json.Marshal(Request{Operation: OpState})
	env := envelope{Version: Version, Seq: 1, Payload: body, MAC: ""}
	codec, _ := NewCodec(bytes.NewReader(nil), bytes.NewBuffer(nil), testKey)
	env.MAC = codec.mac(env.Version, env.Seq, env.Payload)
	raw, _ := json.Marshal(env)
	raw = append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
	var framed bytes.Buffer
	_ = binary.Write(&framed, binary.BigEndian, uint32(len(raw)))
	_, _ = framed.Write(raw)
	reader, _ := NewCodec(&framed, bytes.NewBuffer(nil), testKey)
	var request Request
	if _, err := reader.Read(&request); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error = %v", err)
	}
}

func TestServeOneReturnsValidationAndHandlerErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request Request
		handler handlerFunc
		want    string
	}{
		{"invalid", Request{Operation: OpExec}, handlerFunc(func(context.Context, Request) Response {
			t.Fatal("handler called for invalid request")
			return Response{}
		}), "exec requires"},
		{"too-many-args", Request{Operation: OpExec, Args: make([]string, MaxArguments+1)}, nil, "too many"},
		{"missing-signal", Request{Operation: OpSignal}, nil, "signal is required"},
		{"unsupported", Request{Operation: Operation("future")}, nil, "unsupported operation"},
		{"handler", Request{Operation: OpLogs}, handlerFunc(func(context.Context, Request) Response {
			return Response{Error: "logs unavailable"}
		}), "logs unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, guest := net.Pipe()
			defer host.Close()
			defer guest.Close()
			hostCodec, _ := NewCodec(host, host, testKey)
			guestCodec, _ := NewCodec(guest, guest, testKey)
			done := make(chan error, 1)
			go func() { done <- ServeOne(context.Background(), guestCodec, tc.handler) }()
			_, err := NewClient(hostCodec).Do(context.Background(), tc.request)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClientHonorsAlreadyCancelledContext(t *testing.T) {
	codec, _ := NewCodec(bytes.NewReader(nil), bytes.NewBuffer(nil), testKey)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewClient(codec).Do(ctx, Request{Operation: OpState})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

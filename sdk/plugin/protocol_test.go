package plugin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWriteReadMessageRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	req := Request{ID: "1", Method: "v1.detect", Params: json.RawMessage(`{"path":"."}`)}
	if err := WriteMessage(&buf, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := ReadMessage(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got Request
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != req.ID || got.Method != req.Method || !bytes.Equal(got.Params, req.Params) {
		t.Fatalf("got=%+v want=%+v", got, req)
	}
}

func TestReadMessageReturnsEOFAtCleanBoundary(t *testing.T) {
	if _, err := ReadMessage(bufio.NewReader(strings.NewReader(""))); !errors.Is(err, io.EOF) {
		t.Fatalf("err=%v", err)
	}
}

func TestReadMessageRejectsTruncatedHeader(t *testing.T) {
	_, err := ReadMessage(bufio.NewReader(strings.NewReader("Content-Length: 5")))
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("err=%v", err)
	}
}

func TestReadMessageRejectsMissingContentLength(t *testing.T) {
	input := "Content-Type: " + ContentType + "\r\n\r\n"
	if _, err := ReadMessage(bufio.NewReader(strings.NewReader(input))); err == nil {
		t.Fatal("expected an error for a missing Content-Length")
	}
}

func TestReadMessageRejectsMissingContentType(t *testing.T) {
	input := "Content-Length: 2\r\n\r\n{}"
	if _, err := ReadMessage(bufio.NewReader(strings.NewReader(input))); err == nil {
		t.Fatal("expected an error for a missing Content-Type")
	}
}

func TestReadMessageRejectsWrongContentType(t *testing.T) {
	input := "Content-Type: application/json\r\nContent-Length: 2\r\n\r\n{}"
	if _, err := ReadMessage(bufio.NewReader(strings.NewReader(input))); err == nil {
		t.Fatal("expected an error for the wrong Content-Type")
	}
}

func TestReadMessageRejectsOversizedContentLength(t *testing.T) {
	input := "Content-Length: 999999999999\r\n\r\n"
	if _, err := ReadMessage(bufio.NewReader(strings.NewReader(input))); err == nil {
		t.Fatal("expected an error for an oversized Content-Length")
	}
}

func TestReadMessageBoundsHeadersAndUnterminatedLines(t *testing.T) {
	cases := []string{
		"X-Long: " + strings.Repeat("a", maxHeaderLineBytes+1),
		strings.Repeat("X: a\r\n", maxHeaderBytes/5) + "\r\n",
	}
	for _, input := range cases {
		if _, err := ReadMessage(bufio.NewReaderSize(strings.NewReader(input), maxHeaderBytes*2)); err == nil || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("oversized header accepted: err=%v", err)
		}
	}
}

func TestReadMessageRejectsMalformedHeaderLine(t *testing.T) {
	input := "not a header\r\n\r\n"
	if _, err := ReadMessage(bufio.NewReader(strings.NewReader(input))); err == nil {
		t.Fatal("expected an error for a malformed header line")
	}
}

func TestReadMessageRejectsTruncatedBody(t *testing.T) {
	input := "Content-Type: " + ContentType + "\r\nContent-Length: 10\r\n\r\n{}"
	if _, err := ReadMessage(bufio.NewReader(strings.NewReader(input))); err == nil {
		t.Fatal("expected an error when the body is shorter than Content-Length")
	}
}

func TestWriteMessageRejectsOversizedBody(t *testing.T) {
	huge := Request{ID: "1", Method: "v1.detect", Params: json.RawMessage(`"` + strings.Repeat("a", maxMessageBytes) + `"`)}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, huge); err == nil {
		t.Fatal("expected an error for an oversized message")
	}
}

func TestWriteMessageReportsEncodingAndWriterFailures(t *testing.T) {
	if err := WriteMessage(io.Discard, make(chan int)); err == nil ||
		!strings.Contains(err.Error(), "encode message") {
		t.Fatalf("encoding error=%v", err)
	}
	for _, failAt := range []int{1, 2} {
		writer := &failingWriter{failAt: failAt}
		err := WriteMessage(writer, Request{ID: "1", Method: "v1.detect"})
		if err == nil {
			t.Fatalf("writer failure %d accepted", failAt)
		}
		want := "write header"
		if failAt == 2 {
			want = "write body"
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failure %d err=%v", failAt, err)
		}
	}
}

type failingWriter struct {
	calls  int
	failAt int
}

func (w *failingWriter) Write(payload []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, errors.New("write failed")
	}
	return len(payload), nil
}

func TestRPCErrorImplementsError(t *testing.T) {
	err := &RPCError{Code: 404, Message: "not found"}
	if err.Error() != "not found" {
		t.Fatalf("Error()=%q", err.Error())
	}
}

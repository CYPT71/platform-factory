package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	mux := newMux()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "test-trace")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "ok\n" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "ok\n")
	}
	if rec.Header().Get("X-Request-ID") != "test-trace" {
		t.Fatalf("X-Request-ID = %q", rec.Header().Get("X-Request-ID"))
	}
}

func TestRequestIDIsGeneratedForMissingOrOversizedInput(t *testing.T) {
	for _, input := range []string{"", strings.Repeat("x", 129)} {
		if got := requestID(input); got == "" || got == input {
			t.Fatalf("requestID(%q) = %q", input, got)
		}
	}
	if got := requestID("caller-trace"); got != "caller-trace" {
		t.Fatalf("request ID was not propagated: %q", got)
	}
}

func TestPing(t *testing.T) {
	mux := newMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "pong\n" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "pong\n")
	}
}

func TestMetricsCountsRequestsPerPath(t *testing.T) {
	mux := newMux()
	get := func(path string) {
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	get("/healthz")
	get("/healthz")
	get("/ping")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`http_requests_total{path="/healthz"} 2`,
		`http_requests_total{path="/ping"} 1`,
		`http_requests_total{path="all"} 3`,
		"process_uptime_seconds ",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q, got:\n%s", want, body)
		}
	}
}

func TestDebugExitRejectsNonPost(t *testing.T) {
	originalEnabled := debugExitEnabled
	debugExitEnabled = "true"
	defer func() { debugExitEnabled = originalEnabled }()
	mux := newMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/exit", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestDebugExitRespondsThenCallsExitFunc(t *testing.T) {
	original := exitFunc
	originalEnabled := debugExitEnabled
	debugExitEnabled = "true"
	defer func() {
		exitFunc = original
		debugExitEnabled = originalEnabled
	}()

	exited := make(chan int, 1)
	exitFunc = func(code int) { exited <- code }

	mux := newMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/debug/exit", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if rec.Body.String() != "shutting down\n" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "shutting down\n")
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exitFunc was never called")
	}
}

func TestDebugExitIsDisabledByDefault(t *testing.T) {
	originalEnabled := debugExitEnabled
	debugExitEnabled = "false"
	defer func() { debugExitEnabled = originalEnabled }()
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/debug/exit", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestStatusRecorderDefaultsToOKWithoutExplicitWriteHeader(t *testing.T) {
	mux := newMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func FuzzHTTPHandler(f *testing.F) {
	f.Add(http.MethodGet, "/healthz", "seed-trace", []byte(nil))
	f.Add(http.MethodPost, "/ping", strings.Repeat("x", 129), []byte("payload"))
	f.Add(http.MethodGet, "/metrics", "", []byte(nil))
	f.Add("INVALID", "/missing", "trace", []byte{0x00, 0xff})

	f.Fuzz(func(t *testing.T, method, path, requestIDHeader string, body []byte) {
		if len(method) > 32 || len(path) > 4096 || len(requestIDHeader) > 4096 || len(body) > 64*1024 {
			t.Skip()
		}

		req := &http.Request{
			Method:     method,
			URL:        &url.URL{Path: path},
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			RemoteAddr: "127.0.0.1:12345",
		}
		req.Header.Set("X-Request-ID", requestIDHeader)
		rec := httptest.NewRecorder()

		newMux().ServeHTTP(rec, req)
		result := rec.Result()
		defer result.Body.Close()

		if result.StatusCode < 100 || result.StatusCode > 599 {
			t.Fatalf("invalid HTTP status %d", result.StatusCode)
		}
		responseBody, err := io.ReadAll(io.LimitReader(result.Body, 1<<20))
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		if len(responseBody) >= 1<<20 {
			t.Fatal("response exceeded 1 MiB")
		}
		if got := result.Header.Get("X-Request-ID"); len(got) > 128 {
			t.Fatalf("response request ID exceeds 128 bytes: %d", len(got))
		}
	})
}

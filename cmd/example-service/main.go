// example-service is a tiny stdlib-only HTTP API used as a fixture by the
// local Podman and microVM tooling under scripts/. It is not part of the
// OCI layout builder itself.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

var startTime = time.Now()

type metrics struct {
	total   atomic.Int64
	healthz atomic.Int64
	ping    atomic.Int64
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	n, err := r.ResponseWriter.Write(data)
	r.bytes += n
	return n, err
}

func requestID(header string) string {
	if len(header) > 0 && len(header) <= 128 {
		return header
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Sprintf("request-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(id[:])
}

func observe(path string, counter *atomic.Int64, m *metrics, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		traceID := requestID(r.Header.Get("X-Request-ID"))
		w.Header().Set("X-Request-ID", traceID)
		m.total.Add(1)
		counter.Add(1)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(rec, r)
		slog.Info("request",
			"method", r.Method,
			"path", path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"response_bytes", rec.bytes,
			"trace_id", traceID,
			"remote", r.RemoteAddr,
		)
	}
}

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func pingHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("pong\n"))
}

// exitFunc is os.Exit, indirected so tests can observe a triggered shutdown
// without actually killing the test binary.
var exitFunc = os.Exit
var debugExitEnabled = "false"

// debugExitHandler lets an external caller trigger a self-initiated, orderly
// process exit. It exists so the microVM boot path (scripts/microvm) has a
// way to test a real guest-initiated graceful shutdown end to end: the exit
// happens after the response is sent, giving cmd/microvm-init a normal child
// exit to observe and power the machine off from, the same as any real
// application choosing to stop on its own.
func debugExitHandler(w http.ResponseWriter, r *http.Request) {
	if debugExitEnabled != "true" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	slog.Info("shutdown requested", "component", "example-service", "operation", "debug-exit")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("shutting down\n"))
	go func() {
		time.Sleep(50 * time.Millisecond)
		exitFunc(0)
	}()
}

func metricsHandler(m *metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintln(w, "# HELP http_requests_total Total HTTP requests handled, by path.")
		fmt.Fprintln(w, "# TYPE http_requests_total counter")
		fmt.Fprintf(w, "http_requests_total{path=\"/healthz\"} %d\n", m.healthz.Load())
		fmt.Fprintf(w, "http_requests_total{path=\"/ping\"} %d\n", m.ping.Load())
		fmt.Fprintf(w, "http_requests_total{path=\"all\"} %d\n", m.total.Load())
		fmt.Fprintln(w, "# HELP process_uptime_seconds Seconds since process start.")
		fmt.Fprintln(w, "# TYPE process_uptime_seconds gauge")
		fmt.Fprintf(w, "process_uptime_seconds %f\n", time.Since(startTime).Seconds())
	}
}

func newMux() *http.ServeMux {
	m := &metrics{}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", observe("/healthz", &m.healthz, m, healthzHandler))
	mux.HandleFunc("/ping", observe("/ping", &m.ping, m, pingHandler))
	mux.HandleFunc("/metrics", metricsHandler(m))
	mux.HandleFunc("/debug/exit", debugExitHandler)
	return mux
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	slog.Info("starting server", "component", "example-service", "operation", "serve", "addr", ":8080")
	if err := http.ListenAndServe(":8080", newMux()); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}

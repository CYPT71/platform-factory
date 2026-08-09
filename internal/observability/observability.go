// Package observability provides a unified interface for structured logging,
// metrics, and distributed tracing across the secure-oci project.
// It enables end-to-end visibility into pipeline execution, VMM operations,
// and all other components.
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level represents the severity level of a log entry.
type Level int

// Log levels from lowest to highest severity.
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelDPanic // Panic level but continues execution
	LevelPanic  // Panic level and panics
	LevelNoLevel
)

// String returns the string representation of the log level.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	case LevelDPanic:
		return "DPANIC"
	case LevelPanic:
		return "PANIC"
	default:
		return ""
	}
}

// ParseLevel parses a string into a Level.
func ParseLevel(s string) Level {
	switch s {
	case "DEBUG", "debug":
		return LevelDebug
	case "INFO", "info":
		return LevelInfo
	case "WARN", "warn", "WARNING", "warning":
		return LevelWarn
	case "ERROR", "error":
		return LevelError
	case "DPANIC", "dpanic":
		return LevelDPanic
	case "PANIC", "panic":
		return LevelPanic
	default:
		return LevelInfo
	}
}

// Fields represents a collection of key-value pairs for structured logging.
type Fields map[string]any

// Logger is the interface for structured logging.
type Logger interface {
	// Log writes a log entry with the given level and fields.
	Log(level Level, msg string, fields ...Fields)

	// Convenience methods for each level
	Debug(msg string, fields ...Fields)
	Info(msg string, fields ...Fields)
	Warn(msg string, fields ...Fields)
	Error(msg string, fields ...Fields)
	DPanic(msg string, fields ...Fields)
	Panic(msg string, fields ...Fields)

	// WithFields returns a new Logger with the given fields added to all log entries.
	WithFields(fields Fields) Logger

	// WithContext returns a new Logger with context values extracted from the context.
	WithContext(ctx context.Context) Logger

	// SetLevel sets the minimum log level.
	SetLevel(level Level)

	// GetLevel returns the current minimum log level.
	GetLevel() Level

	// SetOutput sets the output destination for log entries.
	SetOutput(w io.Writer)
}

// defaultLogger is the concrete implementation of Logger.
type defaultLogger struct {
	mu     sync.Mutex
	level  Level
	output io.Writer
	fields Fields

	// hooks for testing
	hooks []func(level Level, msg string, fields Fields)
}

// newDefaultLogger creates a new default logger.
func newDefaultLogger() *defaultLogger {
	return &defaultLogger{
		level:  LevelInfo,
		output: os.Stderr,
		fields: Fields{},
	}
}

// Log implements Logger.
func (l *defaultLogger) Log(level Level, msg string, fields ...Fields) {
	if level < l.level {
		return
	}

	// Merge all fields
	allFields := make(Fields)
	for k, v := range l.fields {
		allFields[k] = v
	}
	for _, f := range fields {
		for k, v := range f {
			allFields[k] = v
		}
	}

	// Add standard fields
	allFields["level"] = level.String()
	allFields["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	allFields["msg"] = msg

	// Execute hooks for testing
	l.mu.Lock()
	for _, hook := range l.hooks {
		hook(level, msg, allFields)
	}
	l.mu.Unlock()

	// Write to output
	l.write(allFields)
}

// write writes the fields to the output.
func (l *defaultLogger) write(fields Fields) {
	data, err := json.Marshal(fields)
	if err != nil {
		// Fallback to simple format if marshaling fails
		fmt.Fprintf(l.output, "%s %s %v\n",
			fields["timestamp"], fields["level"], fields["msg"])
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.output.Write(data)
	l.output.Write([]byte("\n"))
}

// Debug implements Logger.
func (l *defaultLogger) Debug(msg string, fields ...Fields) {
	l.Log(LevelDebug, msg, fields...)
}

// Info implements Logger.
func (l *defaultLogger) Info(msg string, fields ...Fields) {
	l.Log(LevelInfo, msg, fields...)
}

// Warn implements Logger.
func (l *defaultLogger) Warn(msg string, fields ...Fields) {
	l.Log(LevelWarn, msg, fields...)
}

// Error implements Logger.
func (l *defaultLogger) Error(msg string, fields ...Fields) {
	l.Log(LevelError, msg, fields...)
}

// DPanic implements Logger.
func (l *defaultLogger) DPanic(msg string, fields ...Fields) {
	l.Log(LevelDPanic, msg, fields...)
}

// Panic implements Logger.
func (l *defaultLogger) Panic(msg string, fields ...Fields) {
	l.Log(LevelPanic, msg, fields...)
	panic(msg)
}

// WithFields implements Logger.
func (l *defaultLogger) WithFields(fields Fields) Logger {
	newFields := make(Fields)
	for k, v := range l.fields {
		newFields[k] = v
	}
	for k, v := range fields {
		newFields[k] = v
	}
	return &defaultLogger{
		level:  l.level,
		output: l.output,
		fields: newFields,
	}
}

// WithContext implements Logger.
func (l *defaultLogger) WithContext(ctx context.Context) Logger {
	// Extract trace ID from context if present
	if traceID := TraceIDFromContext(ctx); traceID != "" {
		return l.WithFields(Fields{"trace_id": traceID})
	}
	return l
}

// SetLevel implements Logger.
func (l *defaultLogger) SetLevel(level Level) {
	l.level = level
}

// GetLevel implements Logger.
func (l *defaultLogger) GetLevel() Level {
	return l.level
}

// SetOutput implements Logger.
func (l *defaultLogger) SetOutput(w io.Writer) {
	l.output = w
}

// AddHook adds a hook function for testing.
func (l *defaultLogger) AddHook(hook func(level Level, msg string, fields Fields)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hooks = append(l.hooks, hook)
}

// Global logger instance
var globalLogger = newDefaultLogger()

// Default returns the global logger.
func Default() Logger {
	return globalLogger
}

// SetDefault sets the global logger.
func SetDefault(l Logger) {
	globalLogger = l.(*defaultLogger)
}

// SetGlobalLevel sets the level for the global logger.
func SetGlobalLevel(level Level) {
	globalLogger.SetLevel(level)
}

// SetGlobalOutput sets the output for the global logger.
func SetGlobalOutput(w io.Writer) {
	globalLogger.SetOutput(w)
}

// Package-level convenience functions

// Debug logs a debug message.
func Debug(msg string, fields ...Fields) {
	Default().Debug(msg, fields...)
}

// Info logs an info message.
func Info(msg string, fields ...Fields) {
	Default().Info(msg, fields...)
}

// Warn logs a warning message.
func Warn(msg string, fields ...Fields) {
	Default().Warn(msg, fields...)
}

// Error logs an error message.
func Error(msg string, fields ...Fields) {
	Default().Error(msg, fields...)
}

// DPanic logs a panic message but continues execution.
func DPanic(msg string, fields ...Fields) {
	Default().DPanic(msg, fields...)
}

// Panic logs a panic message and panics.
func Panic(msg string, fields ...Fields) {
	Default().Panic(msg, fields...)
}

// Context keys for storing values in context
var (
	contextKeyTraceID  = &contextKey{"trace_id"}
	contextKeySpanID   = &contextKey{"span_id"}
	contextKeyParentID = &contextKey{"parent_id"}
	contextKeyBaggage  = &contextKey{"baggage"}
)

type contextKey struct {
	name string
}

// TraceIDFromContext extracts the trace ID from the context.
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if traceID, ok := ctx.Value(contextKeyTraceID).(string); ok {
		return traceID
	}
	return ""
}

// SpanIDFromContext extracts the span ID from the context.
func SpanIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if spanID, ok := ctx.Value(contextKeySpanID).(string); ok {
		return spanID
	}
	return ""
}

// ParentIDFromContext extracts the parent span ID from the context.
func ParentIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if parentID, ok := ctx.Value(contextKeyParentID).(string); ok {
		return parentID
	}
	return ""
}

// BaggageFromContext extracts the baggage from the context.
func BaggageFromContext(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	if baggage, ok := ctx.Value(contextKeyBaggage).(map[string]string); ok {
		return baggage
	}
	return nil
}

// ContextWithTraceID returns a new context with the given trace ID.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, contextKeyTraceID, traceID)
}

// ContextWithSpanID returns a new context with the given span ID.
func ContextWithSpanID(ctx context.Context, spanID string) context.Context {
	return context.WithValue(ctx, contextKeySpanID, spanID)
}

// ContextWithParentID returns a new context with the given parent span ID.
func ContextWithParentID(ctx context.Context, parentID string) context.Context {
	return context.WithValue(ctx, contextKeyParentID, parentID)
}

// ContextWithBaggage returns a new context with the given baggage.
func ContextWithBaggage(ctx context.Context, baggage map[string]string) context.Context {
	return context.WithValue(ctx, contextKeyBaggage, baggage)
}

// NewContext returns a new context with trace, span, and parent IDs.
func NewContext(ctx context.Context, traceID, spanID, parentID string) context.Context {
	ctx = ContextWithTraceID(ctx, traceID)
	ctx = ContextWithSpanID(ctx, spanID)
	ctx = ContextWithParentID(ctx, parentID)
	return ctx
}

// GenerateID generates a unique identifier for traces and spans.
// In production, this would use a more sophisticated ID generator.
func GenerateID() string {
	// Simple implementation for now - could be enhanced with UUID or other
	return fmt.Sprintf("%s%06d",
		time.Now().UTC().Format("20060102-150405.000000"),
		nextID(),
	)
}

var idCounter uint64

func nextID() uint64 {
	return atomic.AddUint64(&idCounter, 1)
}

// SpanState represents the state of a span.
type SpanState string

const (
	SpanStateStarted  SpanState = "started"
	SpanStateFinished SpanState = "finished"
	SpanStateErrored  SpanState = "errored"
	SpanStatePaused   SpanState = "paused"
)

// Span represents a tracing span for measuring operation duration and context.
type Span struct {
	ID        string         `json:"id"`
	TraceID   string         `json:"trace_id"`
	ParentID  string         `json:"parent_id,omitempty"`
	Name      string         `json:"name"`
	StartTime time.Time      `json:"start_time"`
	EndTime   time.Time      `json:"end_time,omitempty"`
	Duration  time.Duration  `json:"duration,omitempty"`
	State     SpanState      `json:"state"`
	Tags      map[string]any `json:"tags,omitempty"`
	Events    []SpanEvent    `json:"events,omitempty"`
	Error     error          `json:"error,omitempty"`

	// For managing child spans
	children []*Span
	mu       sync.Mutex
}

// SpanEvent represents an event that occurred within a span.
type SpanEvent struct {
	Timestamp time.Time      `json:"timestamp"`
	Name      string         `json:"name"`
	Tags      map[string]any `json:"tags,omitempty"`
}

// SpanOption is a function that configures a Span.
type SpanOption func(*Span)

// WithTag adds a tag to the span.
func WithTag(key string, value any) SpanOption {
	return func(s *Span) {
		if s.Tags == nil {
			s.Tags = make(map[string]any)
		}
		s.Tags[key] = value
	}
}

// WithTags adds multiple tags to the span.
func WithTags(tags map[string]any) SpanOption {
	return func(s *Span) {
		if s.Tags == nil {
			s.Tags = make(map[string]any)
		}
		for k, v := range tags {
			s.Tags[k] = v
		}
	}
}

// SpanContext contains span identifiers for context propagation.
type SpanContext struct {
	TraceID  string            `json:"trace_id"`
	SpanID   string            `json:"span_id"`
	ParentID string            `json:"parent_id,omitempty"`
	Baggage  map[string]string `json:"baggage,omitempty"`
}

// Tracer is the interface for creating and managing spans.
type Tracer interface {
	// StartSpan starts a new span.
	StartSpan(name string, opts ...SpanOption) *Span

	// StartSpanWithContext starts a new span with a parent context.
	StartSpanWithContext(ctx context.Context, name string, opts ...SpanOption) (*Span, context.Context)

	// CurrentSpan returns the current span from the context.
	CurrentSpan(ctx context.Context) *Span

	// Finish ends the current span.
	Finish(span *Span)

	// FinishWithError ends the current span with an error.
	FinishWithError(span *Span, err error)

	// Extract extracts span context from a carrier (e.g., HTTP headers).
	Extract(carrier map[string]string) SpanContext

	// Inject injects span context into a carrier.
	Inject(ctx context.Context, carrier map[string]string)
}

// defaultTracer is the concrete implementation of Tracer.
type defaultTracer struct {
	mu     sync.Mutex
	spans  []*Span
	active map[string]*Span // traceID -> root span
	logger Logger
}

// newDefaultTracer creates a new default tracer.
func newDefaultTracer(logger Logger) *defaultTracer {
	return &defaultTracer{
		spans:  make([]*Span, 0),
		active: make(map[string]*Span),
		logger: logger,
	}
}

// StartSpan implements Tracer.
func (t *defaultTracer) StartSpan(name string, opts ...SpanOption) *Span {
	span, _ := t.StartSpanWithContext(context.Background(), name, opts...)
	return span
}

// StartSpanWithContext implements Tracer.
func (t *defaultTracer) StartSpanWithContext(ctx context.Context, name string, opts ...SpanOption) (*Span, context.Context) {
	// Extract parent context if available
	var traceID, parentID string
	var baggage map[string]string

	if ctx != nil {
		traceID = TraceIDFromContext(ctx)
		parentID = SpanIDFromContext(ctx)
		baggage = BaggageFromContext(ctx)
	}

	// Generate new IDs if needed
	if traceID == "" {
		traceID = GenerateID()
	}
	spanID := GenerateID()

	// Create the span
	span := &Span{
		ID:        spanID,
		TraceID:   traceID,
		ParentID:  parentID,
		Name:      name,
		StartTime: time.Now().UTC(),
		State:     SpanStateStarted,
		Tags:      make(map[string]any),
		Events:    make([]SpanEvent, 0),
		children:  make([]*Span, 0),
	}

	// Apply options
	for _, opt := range opts {
		opt(span)
	}

	// Add trace ID to baggage. BaggageFromContext returns the same map
	// reference stored in ctx, not a copy - mutating it in place would
	// race with every other concurrent span started from the same
	// parent context (as happens under internal/scheduler's worker
	// pool, where every stage's span shares the pipeline-level parent
	// ctx). Always work on a private copy instead.
	copied := make(map[string]string, len(baggage)+1)
	for k, v := range baggage {
		copied[k] = v
	}
	baggage = copied
	baggage["trace_id"] = traceID

	// Store span
	t.mu.Lock()
	t.spans = append(t.spans, span)
	if parentID == "" {
		// This is a root span
		t.active[traceID] = span
	}
	t.mu.Unlock()

	// Create new context
	ctx = ContextWithTraceID(ctx, traceID)
	ctx = ContextWithSpanID(ctx, spanID)
	ctx = ContextWithParentID(ctx, parentID)
	ctx = ContextWithBaggage(ctx, baggage)

	// Log span start
	t.logger.Debug("span started", Fields{
		"span_id":   spanID,
		"trace_id":  traceID,
		"name":      name,
		"parent_id": parentID,
	})

	return span, ctx
}

// CurrentSpan implements Tracer.
func (t *defaultTracer) CurrentSpan(ctx context.Context) *Span {
	if ctx == nil {
		return nil
	}

	traceID := TraceIDFromContext(ctx)
	spanID := SpanIDFromContext(ctx)

	if traceID == "" || spanID == "" {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// For simplicity, we return the root span of the trace
	if root, ok := t.active[traceID]; ok {
		return root
	}
	return nil
}

// Finish implements Tracer.
func (t *defaultTracer) Finish(span *Span) {
	if span == nil {
		return
	}

	span.EndTime = time.Now().UTC()
	span.Duration = span.EndTime.Sub(span.StartTime)
	span.State = SpanStateFinished

	t.logger.Debug("span finished", Fields{
		"span_id":  span.ID,
		"trace_id": span.TraceID,
		"name":     span.Name,
		"duration": span.Duration.String(),
	})
}

// FinishWithError implements Tracer.
func (t *defaultTracer) FinishWithError(span *Span, err error) {
	if span == nil {
		return
	}

	span.EndTime = time.Now().UTC()
	span.Duration = span.EndTime.Sub(span.StartTime)
	span.State = SpanStateErrored
	span.Error = err

	t.logger.Error("span errored", Fields{
		"span_id":  span.ID,
		"trace_id": span.TraceID,
		"name":     span.Name,
		"duration": span.Duration.String(),
		"error":    err.Error(),
	})
}

// Extract implements Tracer.
func (t *defaultTracer) Extract(carrier map[string]string) SpanContext {
	return SpanContext{
		TraceID:  carrier["X-Trace-ID"],
		SpanID:   carrier["X-Span-ID"],
		ParentID: carrier["X-Parent-ID"],
		Baggage:  parseBaggage(carrier["X-Baggage"]),
	}
}

// Inject implements Tracer.
func (t *defaultTracer) Inject(ctx context.Context, carrier map[string]string) {
	if ctx == nil {
		return
	}

	carrier["X-Trace-ID"] = TraceIDFromContext(ctx)
	carrier["X-Span-ID"] = SpanIDFromContext(ctx)
	carrier["X-Parent-ID"] = ParentIDFromContext(ctx)

	baggage := BaggageFromContext(ctx)
	if len(baggage) > 0 {
		carrier["X-Baggage"] = encodeBaggage(baggage)
	}
}

func parseBaggage(s string) map[string]string {
	if s == "" {
		return nil
	}
	result := make(map[string]string)
	// Simple parsing - in production use proper URL encoding
	// Format: key1=value1,key2=value2
	pairs := sort.StringSlice{}
	for _, pair := range sort.StringSlice(strings.Split(s, ",")) {
		if pair != "" {
			pairs = append(pairs, pair)
		}
	}
	pairs.Sort()

	for _, pair := range pairs {
		if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 {
			result[kv[0]] = kv[1]
		}
	}
	return result
}

func encodeBaggage(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	// Sort keys for deterministic output
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, m[k]))
	}
	return strings.Join(parts, ",")
}

// Global tracer instance
var globalTracer = newDefaultTracer(globalLogger)

// DefaultTracer returns the global tracer.
func DefaultTracer() Tracer {
	return globalTracer
}

// SetDefaultTracer sets the global tracer.
func SetDefaultTracer(t Tracer) {
	globalTracer = t.(*defaultTracer)
}

// StartSpan is a package-level convenience for starting a span.
func StartSpan(name string, opts ...SpanOption) *Span {
	span, ctx := DefaultTracer().StartSpanWithContext(context.Background(), name, opts...)
	// We discard ctx as we're just starting a standalone span
	_ = ctx
	return span
}

// StartSpanWithContext is a package-level convenience for starting a span with context.
func StartSpanWithContext(ctx context.Context, name string, opts ...SpanOption) (*Span, context.Context) {
	return DefaultTracer().StartSpanWithContext(ctx, name, opts...)
}

// End ends the given span.
func End(span *Span) {
	DefaultTracer().Finish(span)
}

// EndWithError ends the given span with an error.
func EndWithError(span *Span, err error) {
	DefaultTracer().FinishWithError(span, err)
}

// AddSpanEvent adds an event to a span.
func AddSpanEvent(span *Span, name string, tags map[string]any) {
	if span == nil {
		return
	}

	span.mu.Lock()
	defer span.mu.Unlock()

	span.Events = append(span.Events, SpanEvent{
		Timestamp: time.Now().UTC(),
		Name:      name,
		Tags:      tags,
	})
}

// AddSpanTag adds a tag to a span.
func AddSpanTag(span *Span, key string, value any) {
	if span == nil {
		return
	}

	span.mu.Lock()
	defer span.mu.Unlock()

	if span.Tags == nil {
		span.Tags = make(map[string]any)
	}
	span.Tags[key] = value
}

// Metrics types

// MetricType represents the type of a metric.
type MetricType string

const (
	MetricTypeCounter   MetricType = "counter"
	MetricTypeGauge     MetricType = "gauge"
	MetricTypeHistogram MetricType = "histogram"
)

// Metric represents a single metric value.
type Metric struct {
	Name        string            `json:"name"`
	Type        MetricType        `json:"type"`
	Value       float64           `json:"value"`
	Labels      map[string]string `json:"labels,omitempty"`
	Timestamp   time.Time         `json:"timestamp"`
	Description string            `json:"description,omitempty"`
}

// MetricsRegistry is the interface for registering and recording metrics.
type MetricsRegistry interface {
	// Counter operations
	Inc(name string, labels ...map[string]string) error
	IncBy(name string, value float64, labels ...map[string]string) error

	// Gauge operations
	Set(name string, value float64, labels ...map[string]string) error
	Add(name string, value float64, labels ...map[string]string) error

	// Histogram operations
	Observe(name string, value float64, labels ...map[string]string) error

	// Registry operations
	RegisterCounter(name, description string) error
	RegisterGauge(name, description string) error
	RegisterHistogram(name, description string, buckets []float64) error

	// Get all metrics
	GetMetrics() []Metric
}

// defaultMetricsRegistry is the concrete implementation of MetricsRegistry.
type defaultMetricsRegistry struct {
	mu         sync.Mutex
	counters   map[string]*counterMetric
	gauges     map[string]*gaugeMetric
	histograms map[string]*histogramMetric
}

type counterMetric struct {
	name        string
	description string
	labels      map[string]string
	value       float64
}

type gaugeMetric struct {
	name        string
	description string
	labels      map[string]string
	value       float64
}

type histogramMetric struct {
	name        string
	description string
	buckets     []float64
	labels      map[string]string
	values      []float64 // counts for each bucket
	total       float64
	count       int64
}

// newDefaultMetricsRegistry creates a new default metrics registry.
func newDefaultMetricsRegistry() *defaultMetricsRegistry {
	return &defaultMetricsRegistry{
		counters:   make(map[string]*counterMetric),
		gauges:     make(map[string]*gaugeMetric),
		histograms: make(map[string]*histogramMetric),
	}
}

// Inc implements MetricsRegistry.
func (r *defaultMetricsRegistry) Inc(name string, labels ...map[string]string) error {
	return r.IncBy(name, 1, labels...)
}

// IncBy implements MetricsRegistry.
func (r *defaultMetricsRegistry) IncBy(name string, value float64, labels ...map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.counters[name]; !ok {
		return fmt.Errorf("counter %s not registered", name)
	}

	// For simplicity, we just increment the counter
	// In production, we'd handle labels properly
	r.counters[name].value += value
	return nil
}

// Set implements MetricsRegistry.
func (r *defaultMetricsRegistry) Set(name string, value float64, labels ...map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.gauges[name]; !ok {
		return fmt.Errorf("gauge %s not registered", name)
	}

	r.gauges[name].value = value
	return nil
}

// Add implements MetricsRegistry.
func (r *defaultMetricsRegistry) Add(name string, value float64, labels ...map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.gauges[name]; !ok {
		return fmt.Errorf("gauge %s not registered", name)
	}

	r.gauges[name].value += value
	return nil
}

// Observe implements MetricsRegistry.
func (r *defaultMetricsRegistry) Observe(name string, value float64, labels ...map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.histograms[name]; !ok {
		return fmt.Errorf("histogram %s not registered", name)
	}

	h := r.histograms[name]
	h.total += value
	h.count++

	// Find the appropriate bucket
	for i, bucket := range h.buckets {
		if value <= bucket {
			if int(i) >= len(h.values) {
				h.values = append(h.values, 0)
			}
			h.values[i]++
			break
		}
	}

	return nil
}

// RegisterCounter implements MetricsRegistry.
func (r *defaultMetricsRegistry) RegisterCounter(name, description string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.counters[name]; ok {
		return fmt.Errorf("counter %s already registered", name)
	}

	r.counters[name] = &counterMetric{
		name:        name,
		description: description,
		labels:      make(map[string]string),
	}
	return nil
}

// RegisterGauge implements MetricsRegistry.
func (r *defaultMetricsRegistry) RegisterGauge(name, description string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.gauges[name]; ok {
		return fmt.Errorf("gauge %s already registered", name)
	}

	r.gauges[name] = &gaugeMetric{
		name:        name,
		description: description,
		labels:      make(map[string]string),
	}
	return nil
}

// RegisterHistogram implements MetricsRegistry.
func (r *defaultMetricsRegistry) RegisterHistogram(name, description string, buckets []float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.histograms[name]; ok {
		return fmt.Errorf("histogram %s already registered", name)
	}

	r.histograms[name] = &histogramMetric{
		name:        name,
		description: description,
		buckets:     buckets,
		labels:      make(map[string]string),
		values:      make([]float64, len(buckets)),
	}
	return nil
}

// GetMetrics implements MetricsRegistry.
func (r *defaultMetricsRegistry) GetMetrics() []Metric {
	r.mu.Lock()
	defer r.mu.Unlock()

	metrics := make([]Metric, 0)

	// Add counters
	for name, c := range r.counters {
		metrics = append(metrics, Metric{
			Name:        name,
			Type:        MetricTypeCounter,
			Value:       c.value,
			Labels:      c.labels,
			Timestamp:   time.Now().UTC(),
			Description: c.description,
		})
	}

	// Add gauges
	for name, g := range r.gauges {
		metrics = append(metrics, Metric{
			Name:        name,
			Type:        MetricTypeGauge,
			Value:       g.value,
			Labels:      g.labels,
			Timestamp:   time.Now().UTC(),
			Description: g.description,
		})
	}

	// Add histograms
	for name, h := range r.histograms {
		// For simplicity, we just return the total
		// In production, we'd return all bucket values
		metrics = append(metrics, Metric{
			Name:        name + "_total",
			Type:        MetricTypeGauge,
			Value:       h.total,
			Labels:      h.labels,
			Timestamp:   time.Now().UTC(),
			Description: h.description + " total",
		})
		metrics = append(metrics, Metric{
			Name:        name + "_count",
			Type:        MetricTypeCounter,
			Value:       float64(h.count),
			Labels:      h.labels,
			Timestamp:   time.Now().UTC(),
			Description: h.description + " count",
		})
	}

	return metrics
}

// Global metrics registry instance
var globalMetricsRegistry = newDefaultMetricsRegistry()

// DefaultMetrics returns the global metrics registry.
func DefaultMetrics() MetricsRegistry {
	return globalMetricsRegistry
}

// SetDefaultMetrics sets the global metrics registry.
func SetDefaultMetrics(r MetricsRegistry) {
	globalMetricsRegistry = r.(*defaultMetricsRegistry)
}

// Package-level metric functions

// Inc increments a counter metric.
func Inc(name string, labels ...map[string]string) error {
	return DefaultMetrics().Inc(name, labels...)
}

// IncBy increments a counter metric by the given value.
func IncBy(name string, value float64, labels ...map[string]string) error {
	return DefaultMetrics().IncBy(name, value, labels...)
}

// Set sets a gauge metric to the given value.
func Set(name string, value float64, labels ...map[string]string) error {
	return DefaultMetrics().Set(name, value, labels...)
}

// Add adds to a gauge metric.
func Add(name string, value float64, labels ...map[string]string) error {
	return DefaultMetrics().Add(name, value, labels...)
}

// Observe records a value in a histogram.
func Observe(name string, value float64, labels ...map[string]string) error {
	return DefaultMetrics().Observe(name, value, labels...)
}

// RegisterCounter registers a counter metric.
func RegisterCounter(name, description string) error {
	return DefaultMetrics().RegisterCounter(name, description)
}

// RegisterGauge registers a gauge metric.
func RegisterGauge(name, description string) error {
	return DefaultMetrics().RegisterGauge(name, description)
}

// RegisterHistogram registers a histogram metric.
func RegisterHistogram(name, description string, buckets []float64) error {
	return DefaultMetrics().RegisterHistogram(name, description, buckets)
}

// GetAllMetrics returns all registered metrics.
func GetAllMetrics() []Metric {
	return DefaultMetrics().GetMetrics()
}

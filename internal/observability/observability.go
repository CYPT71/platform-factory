// Package observability provides structured logging, metrics, and tracing.
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level represents the severity level of a log entry.
type Level int

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
	if l >= LevelDebug && l <= LevelPanic {
		return [...]string{"DEBUG", "INFO", "WARN", "ERROR", "DPANIC", "PANIC"}[l]
	}
	return ""
}

// ParseLevel parses a string into a Level.
func ParseLevel(s string) Level {
	if strings.EqualFold(s, "WARNING") {
		s = "WARN"
	}
	for level := LevelDebug; level <= LevelPanic; level++ {
		if strings.EqualFold(s, level.String()) {
			return level
		}
	}
	return LevelInfo
}

// Fields represents a collection of key-value pairs for structured logging.
type Fields map[string]any

// Logger is the interface for structured logging.
type Logger interface {
	// Log writes a log entry with the given level and fields.
	Log(level Level, msg string, fields ...Fields)

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

type defaultLogger struct {
	mu     sync.Mutex
	level  Level
	output io.Writer
	fields Fields

	hooks []func(level Level, msg string, fields Fields)
}

func newDefaultLogger() *defaultLogger {
	return &defaultLogger{
		level:  LevelInfo,
		output: os.Stderr,
		fields: Fields{},
	}
}

func (l *defaultLogger) Log(level Level, msg string, fields ...Fields) {
	if level < l.level {
		return
	}

	allFields := make(Fields)
	for k, v := range l.fields {
		allFields[k] = redactField(k, v)
	}
	for _, f := range fields {
		for k, v := range f {
			allFields[k] = redactField(k, v)
		}
	}

	allFields["level"] = level.String()
	allFields["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	allFields["msg"] = msg

	l.mu.Lock()
	for _, hook := range l.hooks {
		hook(level, msg, allFields)
	}
	l.mu.Unlock()

	l.write(allFields)
}

const redactedValue = "[redacted]"

func redactField(key string, value any) any {
	if sensitiveFieldKey(key) {
		return redactedValue
	}
	return redactValue(value)
}

func sensitiveFieldKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", ".", "_").Replace(key))
	for _, marker := range []string{"password", "passwd", "secret", "token", "authorization", "credential", "private_key", "client_key", "cookie"} {
		if normalized == marker || strings.HasSuffix(normalized, "_"+marker) || strings.HasPrefix(normalized, marker+"_") {
			return true
		}
	}
	return false
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case Fields:
		return redactMap(map[string]any(typed))
	case map[string]any:
		return redactMap(typed)
	case map[string]string:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = redactField(key, item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = redactValue(item)
		}
		return result
	case string:
		return redactURLCredentials(typed)
	case error:
		return redactURLCredentials(typed.Error())
	}
	reflection := reflect.ValueOf(value)
	if reflection.IsValid() && (reflection.Kind() == reflect.Slice || reflection.Kind() == reflect.Array) {
		result := make([]any, reflection.Len())
		for index := range result {
			result[index] = redactValue(reflection.Index(index).Interface())
		}
		return result
	}
	return value
}

func redactMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = redactField(key, item)
	}
	return result
}

func redactURLCredentials(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return value
	}
	if parsed.User != nil {
		parsed.User = url.User(redactedValue)
	}
	query := parsed.Query()
	changed := parsed.User != nil
	for key := range query {
		if sensitiveFieldKey(key) {
			query.Set(key, redactedValue)
			changed = true
		}
	}
	if !changed {
		return value
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (l *defaultLogger) write(fields Fields) {
	data, err := json.Marshal(fields)
	if err != nil {
		fmt.Fprintf(l.output, "%s %s %v\n",
			fields["timestamp"], fields["level"], fields["msg"])
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.output.Write(data)
	l.output.Write([]byte("\n"))
}

func (l *defaultLogger) Debug(msg string, fields ...Fields) {
	l.Log(LevelDebug, msg, fields...)
}

func (l *defaultLogger) Info(msg string, fields ...Fields) {
	l.Log(LevelInfo, msg, fields...)
}

func (l *defaultLogger) Warn(msg string, fields ...Fields) {
	l.Log(LevelWarn, msg, fields...)
}

func (l *defaultLogger) Error(msg string, fields ...Fields) {
	l.Log(LevelError, msg, fields...)
}

func (l *defaultLogger) DPanic(msg string, fields ...Fields) {
	l.Log(LevelDPanic, msg, fields...)
}

func (l *defaultLogger) Panic(msg string, fields ...Fields) {
	l.Log(LevelPanic, msg, fields...)
	panic(msg)
}

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

func (l *defaultLogger) WithContext(ctx context.Context) Logger {
	if traceID := TraceIDFromContext(ctx); traceID != "" {
		return l.WithFields(Fields{"trace_id": traceID})
	}
	return l
}

func (l *defaultLogger) SetLevel(level Level) {
	l.level = level
}

func (l *defaultLogger) GetLevel() Level {
	return l.level
}

func (l *defaultLogger) SetOutput(w io.Writer) {
	l.output = w
}

// AddHook adds a hook function for testing.
func (l *defaultLogger) AddHook(hook func(level Level, msg string, fields Fields)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hooks = append(l.hooks, hook)
}

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
	return contextValue[string](ctx, contextKeyTraceID)
}

// SpanIDFromContext extracts the span ID from the context.
func SpanIDFromContext(ctx context.Context) string {
	return contextValue[string](ctx, contextKeySpanID)
}

// ParentIDFromContext extracts the parent span ID from the context.
func ParentIDFromContext(ctx context.Context) string {
	return contextValue[string](ctx, contextKeyParentID)
}

// BaggageFromContext extracts the baggage from the context.
func BaggageFromContext(ctx context.Context) map[string]string {
	return contextValue[map[string]string](ctx, contextKeyBaggage)
}

func contextValue[T any](ctx context.Context, key *contextKey) (zero T) {
	if ctx == nil {
		return zero
	}
	if value, ok := ctx.Value(key).(T); ok {
		return value
	}
	return zero
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

// GenerateID returns a process-unique, time-sortable identifier.
func GenerateID() string {
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

type defaultTracer struct {
	mu     sync.Mutex
	spans  []*Span
	active map[string]*Span // traceID -> root span
	logger Logger
}

func newDefaultTracer(logger Logger) *defaultTracer {
	return &defaultTracer{
		spans:  make([]*Span, 0),
		active: make(map[string]*Span),
		logger: logger,
	}
}

func (t *defaultTracer) StartSpan(name string, opts ...SpanOption) *Span {
	span, _ := t.StartSpanWithContext(context.Background(), name, opts...)
	return span
}

func (t *defaultTracer) StartSpanWithContext(ctx context.Context, name string, opts ...SpanOption) (*Span, context.Context) {
	var traceID, parentID string
	var baggage map[string]string

	if ctx != nil {
		traceID = TraceIDFromContext(ctx)
		parentID = SpanIDFromContext(ctx)
		baggage = BaggageFromContext(ctx)
	}

	if traceID == "" {
		traceID = GenerateID()
	}
	spanID := GenerateID()

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

	t.mu.Lock()
	t.spans = append(t.spans, span)
	if parentID == "" {
		t.active[traceID] = span
	}
	t.mu.Unlock()

	ctx = ContextWithTraceID(ctx, traceID)
	ctx = ContextWithSpanID(ctx, spanID)
	ctx = ContextWithParentID(ctx, parentID)
	ctx = ContextWithBaggage(ctx, baggage)

	t.logger.Debug("span started", Fields{
		"span_id":   spanID,
		"trace_id":  traceID,
		"name":      name,
		"parent_id": parentID,
	})

	return span, ctx
}

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

	if root, ok := t.active[traceID]; ok {
		return root
	}
	return nil
}

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

func (t *defaultTracer) Extract(carrier map[string]string) SpanContext {
	return SpanContext{
		TraceID:  carrier["X-Trace-ID"],
		SpanID:   carrier["X-Span-ID"],
		ParentID: carrier["X-Parent-ID"],
		Baggage:  parseBaggage(carrier["X-Baggage"]),
	}
}

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
	pairs := strings.Split(s, ",")
	sort.Strings(pairs)
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
	span, _ := DefaultTracer().StartSpanWithContext(context.Background(), name, opts...)
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
	Inc(name string, labels ...map[string]string) error
	IncBy(name string, value float64, labels ...map[string]string) error

	Set(name string, value float64, labels ...map[string]string) error
	Add(name string, value float64, labels ...map[string]string) error

	Observe(name string, value float64, labels ...map[string]string) error

	RegisterCounter(name, description string) error
	RegisterGauge(name, description string) error
	RegisterHistogram(name, description string, buckets []float64) error

	GetMetrics() []Metric
}

type defaultMetricsRegistry struct {
	mu      sync.Mutex
	metrics map[metricKey]*registeredMetric
}

type metricKey struct {
	type_ MetricType
	name  string
}

type registeredMetric struct {
	name        string
	type_       MetricType
	description string
	labels      map[string]string
	value       float64
	buckets     []float64
	values      []float64
	total       float64
	count       int64
}

func newDefaultMetricsRegistry() *defaultMetricsRegistry {
	return &defaultMetricsRegistry{
		metrics: make(map[metricKey]*registeredMetric),
	}
}

func (r *defaultMetricsRegistry) Inc(name string, labels ...map[string]string) error {
	return r.IncBy(name, 1, labels...)
}

func (r *defaultMetricsRegistry) IncBy(name string, value float64, labels ...map[string]string) error {
	return r.mutate(MetricTypeCounter, name, func(metric *registeredMetric) { metric.value += value })
}

func (r *defaultMetricsRegistry) Set(name string, value float64, labels ...map[string]string) error {
	return r.mutate(MetricTypeGauge, name, func(metric *registeredMetric) { metric.value = value })
}

func (r *defaultMetricsRegistry) Add(name string, value float64, labels ...map[string]string) error {
	return r.mutate(MetricTypeGauge, name, func(metric *registeredMetric) { metric.value += value })
}

func (r *defaultMetricsRegistry) Observe(name string, value float64, labels ...map[string]string) error {
	return r.mutate(MetricTypeHistogram, name, func(metric *registeredMetric) {
		metric.total += value
		metric.count++
		for i, bucket := range metric.buckets {
			if value <= bucket {
				metric.values[i]++
				break
			}
		}
	})
}

func (r *defaultMetricsRegistry) RegisterCounter(name, description string) error {
	return r.register(MetricTypeCounter, name, description, nil)
}

func (r *defaultMetricsRegistry) RegisterGauge(name, description string) error {
	return r.register(MetricTypeGauge, name, description, nil)
}

func (r *defaultMetricsRegistry) RegisterHistogram(name, description string, buckets []float64) error {
	return r.register(MetricTypeHistogram, name, description, buckets)
}

func (r *defaultMetricsRegistry) register(kind MetricType, name, description string, buckets []float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := metricKey{kind, name}
	if _, ok := r.metrics[key]; ok {
		return fmt.Errorf("%s %s already registered", kind, name)
	}
	r.metrics[key] = &registeredMetric{
		name: name, type_: kind, description: description,
		labels: make(map[string]string), buckets: append([]float64(nil), buckets...),
		values: make([]float64, len(buckets)),
	}
	return nil
}

func (r *defaultMetricsRegistry) mutate(kind MetricType, name string, apply func(*registeredMetric)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	metric, ok := r.metrics[metricKey{kind, name}]
	if !ok {
		return fmt.Errorf("%s %s not registered", kind, name)
	}
	apply(metric)
	return nil
}

func (r *defaultMetricsRegistry) GetMetrics() []Metric {
	r.mu.Lock()
	defer r.mu.Unlock()

	metrics := make([]Metric, 0, len(r.metrics))
	now := time.Now().UTC()
	for _, registered := range r.metrics {
		if registered.type_ != MetricTypeHistogram {
			metrics = append(metrics, registered.snapshot(registered.name, registered.type_, registered.value, registered.description, now))
			continue
		}
		metrics = append(metrics,
			registered.snapshot(registered.name+"_total", MetricTypeGauge, registered.total, registered.description+" total", now),
			registered.snapshot(registered.name+"_count", MetricTypeCounter, float64(registered.count), registered.description+" count", now),
		)
	}
	return metrics
}

func (m *registeredMetric) snapshot(name string, kind MetricType, value float64, description string, timestamp time.Time) Metric {
	return Metric{Name: name, Type: kind, Value: value, Labels: m.labels, Timestamp: timestamp, Description: description}
}

var globalMetricsRegistry = newDefaultMetricsRegistry()

// DefaultMetrics returns the global metrics registry.
func DefaultMetrics() MetricsRegistry {
	return globalMetricsRegistry
}

// SetDefaultMetrics sets the global metrics registry.
func SetDefaultMetrics(r MetricsRegistry) {
	globalMetricsRegistry = r.(*defaultMetricsRegistry)
}

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

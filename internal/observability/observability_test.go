package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected Level
	}{
		{"DEBUG", LevelDebug},
		{"debug", LevelDebug},
		{"INFO", LevelInfo},
		{"info", LevelInfo},
		{"WARN", LevelWarn},
		{"warn", LevelWarn},
		{"WARNING", LevelWarn},
		{"warning", LevelWarn},
		{"ERROR", LevelError},
		{"error", LevelError},
		{"DPANIC", LevelDPanic},
		{"dpanic", LevelDPanic},
		{"PANIC", LevelPanic},
		{"panic", LevelPanic},
		{"unknown", LevelInfo},
		{"", LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseLevel(tt.input)
			if got != tt.expected {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestLevelString(t *testing.T) {
	tests := []struct {
		level    Level
		expected string
	}{
		{LevelDebug, "DEBUG"},
		{LevelInfo, "INFO"},
		{LevelWarn, "WARN"},
		{LevelError, "ERROR"},
		{LevelDPanic, "DPANIC"},
		{LevelPanic, "PANIC"},
		{LevelNoLevel, ""},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := tt.level.String()
			if got != tt.expected {
				t.Errorf("Level(%v).String() = %q, want %q", tt.level, got, tt.expected)
			}
		})
	}
}

func TestDefaultLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := newDefaultLogger()
	logger.SetOutput(&buf)
	logger.SetLevel(LevelDebug)

	// Test basic logging
	logger.Info("test message", Fields{"key": "value"})

	output := buf.String()
	if !strings.Contains(output, "INFO") {
		t.Error("expected output to contain INFO level")
	}
	if !strings.Contains(output, "test message") {
		t.Error("expected output to contain message")
	}
	if !strings.Contains(output, "key") {
		t.Error("expected output to contain field key")
	}

	// Test with fields
	logger2 := logger.WithFields(Fields{"component": "test"})
	logger2.Info("component message")

	output2 := buf.String()
	if !strings.Contains(output2, "component") {
		t.Error("expected output to contain component field")
	}
}

func TestLoggerWithContext(t *testing.T) {
	var buf bytes.Buffer
	logger := newDefaultLogger()
	logger.SetOutput(&buf)
	logger.SetLevel(LevelDebug)

	ctx := context.Background()
	ctx = ContextWithTraceID(ctx, "test-trace-123")

	logger2 := logger.WithContext(ctx)
	logger2.Info("context test")

	output := buf.String()
	if !strings.Contains(output, "test-trace-123") {
		t.Error("expected output to contain trace ID from context")
	}
}

func TestLoggerLevels(t *testing.T) {
	var buf bytes.Buffer
	logger := newDefaultLogger()
	logger.SetOutput(&buf)

	// Set level to Warn - Debug and Info should be filtered
	logger.SetLevel(LevelWarn)

	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")

	output := buf.String()
	if strings.Contains(output, "debug message") {
		t.Error("debug message should be filtered")
	}
	if strings.Contains(output, "info message") {
		t.Error("info message should be filtered")
	}
	if !strings.Contains(output, "warn message") {
		t.Error("warn message should be present")
	}
}

func TestLoggerJSONOutput(t *testing.T) {
	var buf bytes.Buffer
	logger := newDefaultLogger()
	logger.SetOutput(&buf)
	logger.SetLevel(LevelDebug)

	logger.Info("json test", Fields{
		"string_field": "value",
		"int_field":    42,
		"float_field":  3.14,
		"bool_field":   true,
	})

	output := buf.String()
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result["msg"] != "json test" {
		t.Errorf("expected msg=json test, got %v", result["msg"])
	}
	if result["string_field"] != "value" {
		t.Errorf("expected string_field=value, got %v", result["string_field"])
	}
}

func TestLoggerHooks(t *testing.T) {
	logger := newDefaultLogger()
	logger.SetLevel(LevelDebug)

	var capturedLevel Level
	var capturedMsg string
	var capturedFields Fields

	logger.AddHook(func(level Level, msg string, fields Fields) {
		capturedLevel = level
		capturedMsg = msg
		capturedFields = fields
	})

	logger.Info("hook test", Fields{"test": "value"})

	if capturedLevel != LevelInfo {
		t.Errorf("expected captured level to be Info, got %v", capturedLevel)
	}
	if capturedMsg != "hook test" {
		t.Errorf("expected captured msg to be hook test, got %q", capturedMsg)
	}
	if capturedFields["test"] != "value" {
		t.Errorf("expected captured fields to contain test=value, got %v", capturedFields)
	}
}

func TestLoggerRedactsSensitiveFieldsBeforeHooksAndJSON(t *testing.T) {
	var output bytes.Buffer
	logger := newDefaultLogger()
	logger.SetOutput(&output)
	logger.SetLevel(LevelDebug)
	var hooked Fields
	logger.AddHook(func(_ Level, _ string, fields Fields) { hooked = fields })
	original := map[string]any{"nested": map[string]any{"access_token": "sentinel-token"}}
	logger.Info("safe", Fields{
		"password": "sentinel-password", "details": original,
		"endpoint": "https://user:sentinel-url@example.test/path?token=sentinel-query&ok=yes",
	})
	logger.WithFields(Fields{"client_secret": "sentinel-persistent"}).Info("also safe")
	serialized := output.String()
	for _, secret := range []string{"sentinel-password", "sentinel-token", "sentinel-url", "sentinel-query", "sentinel-persistent"} {
		if strings.Contains(serialized, secret) || strings.Contains(fmt.Sprint(hooked), secret) {
			t.Fatalf("secret %q reached output or hook: output=%s hook=%v", secret, serialized, hooked)
		}
	}
	if original["nested"].(map[string]any)["access_token"] != "sentinel-token" {
		t.Fatal("redaction mutated caller-owned fields")
	}
	if !strings.Contains(serialized, "[redacted]") {
		t.Fatalf("redaction marker absent: %s", serialized)
	}
}

func TestPanicLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := newDefaultLogger()
	logger.SetOutput(&buf)
	logger.SetLevel(LevelDebug)

	// Test DPanic - should not panic
	logger.DPanic("dpanic message")

	// Test Panic - should panic
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected Panic to panic")
		}
	}()

	logger.Panic("panic message")
}

func TestGlobalLoggerFunctions(t *testing.T) {
	// Test package-level functions
	SetGlobalLevel(LevelDebug)
	DeferResetGlobalLogger()

	var buf bytes.Buffer
	SetGlobalOutput(&buf)

	Info("global info test")
	Error("global error test")

	output := buf.String()
	if !strings.Contains(output, "global info test") {
		t.Error("expected output to contain global info test")
	}
	if !strings.Contains(output, "global error test") {
		t.Error("expected output to contain global error test")
	}
}

// DeferResetGlobalLogger resets the global logger after the test.
func DeferResetGlobalLogger() func() {
	oldLogger := globalLogger
	return func() {
		globalLogger = oldLogger
	}
}

func TestContextPropagation(t *testing.T) {
	ctx := context.Background()

	ctx = ContextWithTraceID(ctx, "trace-1")
	ctx = ContextWithSpanID(ctx, "span-1")
	ctx = ContextWithParentID(ctx, "parent-1")
	ctx = ContextWithBaggage(ctx, map[string]string{"key": "value"})

	if TraceIDFromContext(ctx) != "trace-1" {
		t.Error("expected trace ID to be trace-1")
	}
	if SpanIDFromContext(ctx) != "span-1" {
		t.Error("expected span ID to be span-1")
	}
	if ParentIDFromContext(ctx) != "parent-1" {
		t.Error("expected parent ID to be parent-1")
	}

	baggage := BaggageFromContext(ctx)
	if baggage["key"] != "value" {
		t.Errorf("expected baggage[key]=value, got %v", baggage["key"])
	}
}

func TestNewContext(t *testing.T) {
	ctx := NewContext(context.Background(), "trace-1", "span-1", "parent-1")

	if TraceIDFromContext(ctx) != "trace-1" {
		t.Error("expected trace ID to be trace-1")
	}
	if SpanIDFromContext(ctx) != "span-1" {
		t.Error("expected span ID to be span-1")
	}
	if ParentIDFromContext(ctx) != "parent-1" {
		t.Error("expected parent ID to be parent-1")
	}
}

func TestNilContext(t *testing.T) {
	// All FromContext functions should handle nil gracefully
	if TraceIDFromContext(nil) != "" {
		t.Error("expected TraceIDFromContext(nil) to return empty string")
	}
	if SpanIDFromContext(nil) != "" {
		t.Error("expected SpanIDFromContext(nil) to return empty string")
	}
	if ParentIDFromContext(nil) != "" {
		t.Error("expected ParentIDFromContext(nil) to return empty string")
	}
	if BaggageFromContext(nil) != nil {
		t.Error("expected BaggageFromContext(nil) to return nil")
	}
}

func TestGenerateID(t *testing.T) {
	// Just test that it generates non-empty IDs
	for i := 0; i < 10; i++ {
		id := GenerateID()
		if id == "" {
			t.Error("expected non-empty ID")
		}
		// Check that ID contains current year format (2026)
		if !strings.Contains(id, "2026") {
			t.Logf("Generated ID: %s", id)
			t.Error("expected ID to contain year")
		}
	}
}

func TestDefaultTracer(t *testing.T) {
	tracer := newDefaultTracer(newDefaultLogger())

	span, ctx := tracer.StartSpanWithContext(context.Background(), "test-span")

	if span == nil {
		t.Fatal("expected non-nil span")
	}
	if span.Name != "test-span" {
		t.Errorf("expected span name to be test-span, got %q", span.Name)
	}
	if span.ID == "" {
		t.Error("expected non-empty span ID")
	}
	if span.TraceID == "" {
		t.Error("expected non-empty trace ID")
	}

	// Test context propagation
	if TraceIDFromContext(ctx) != span.TraceID {
		t.Error("expected trace ID in context to match span trace ID")
	}
	if SpanIDFromContext(ctx) != span.ID {
		t.Error("expected span ID in context to match span ID")
	}
}

func TestTracerFinish(t *testing.T) {
	tracer := newDefaultTracer(newDefaultLogger())

	span, _ := tracer.StartSpanWithContext(context.Background(), "test-span")
	time.Sleep(10 * time.Millisecond) // Small delay to ensure duration > 0

	tracer.Finish(span)

	if span.State != SpanStateFinished {
		t.Errorf("expected span state to be finished, got %q", span.State)
	}
	if span.EndTime.IsZero() {
		t.Error("expected end time to be set")
	}
	if span.Duration <= 0 {
		t.Error("expected positive duration")
	}
}

func TestTracerFinishWithError(t *testing.T) {
	tracer := newDefaultTracer(newDefaultLogger())

	testErr := errors.New("test error")
	span, _ := tracer.StartSpanWithContext(context.Background(), "error-span")

	tracer.FinishWithError(span, testErr)

	if span.State != SpanStateErrored {
		t.Errorf("expected span state to be errored, got %q", span.State)
	}
	if span.Error != testErr {
		t.Errorf("expected span error to be testErr")
	}
}

func TestSpanOptions(t *testing.T) {
	tracer := newDefaultTracer(newDefaultLogger())

	span, _ := tracer.StartSpanWithContext(context.Background(), "option-span",
		WithTag("key1", "value1"),
		WithTags(map[string]any{"key2": "value2", "key3": 42}),
	)

	if span.Tags["key1"] != "value1" {
		t.Errorf("expected key1=value1, got %v", span.Tags["key1"])
	}
	if span.Tags["key2"] != "value2" {
		t.Errorf("expected key2=value2, got %v", span.Tags["key2"])
	}
	if span.Tags["key3"] != 42 {
		t.Errorf("expected key3=42, got %v", span.Tags["key3"])
	}
}

func TestAddSpanEvent(t *testing.T) {
	tracer := newDefaultTracer(newDefaultLogger())

	span, _ := tracer.StartSpanWithContext(context.Background(), "event-span")

	AddSpanEvent(span, "test-event", map[string]any{"event_key": "event_value"})

	if len(span.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(span.Events))
	}
	if span.Events[0].Name != "test-event" {
		t.Errorf("expected event name to be test-event, got %q", span.Events[0].Name)
	}
	if span.Events[0].Tags["event_key"] != "event_value" {
		t.Errorf("expected event tag event_key=event_value, got %v", span.Events[0].Tags)
	}
}

func TestAddSpanTag(t *testing.T) {
	tracer := newDefaultTracer(newDefaultLogger())

	span, _ := tracer.StartSpanWithContext(context.Background(), "tag-span")

	AddSpanTag(span, "tag-key", "tag-value")

	if span.Tags["tag-key"] != "tag-value" {
		t.Errorf("expected tag-key=tag-value, got %v", span.Tags["tag-key"])
	}
}

func TestTracerExtractInject(t *testing.T) {
	tracer := newDefaultTracer(newDefaultLogger())

	// Create a context with span
	span, ctx := tracer.StartSpanWithContext(context.Background(), "extract-test")
	_ = span

	// Inject into carrier
	carrier := make(map[string]string)
	tracer.Inject(ctx, carrier)

	if carrier["X-Trace-ID"] == "" {
		t.Error("expected X-Trace-ID in carrier")
	}
	if carrier["X-Span-ID"] == "" {
		t.Error("expected X-Span-ID in carrier")
	}

	// Extract from carrier
	extracted := tracer.Extract(carrier)
	if extracted.TraceID != carrier["X-Trace-ID"] {
		t.Error("expected extracted trace ID to match carrier")
	}
	if extracted.SpanID != carrier["X-Span-ID"] {
		t.Error("expected extracted span ID to match carrier")
	}
}

func TestMetricsRegistry(t *testing.T) {
	registry := newDefaultMetricsRegistry()

	// Register metrics
	if err := registry.RegisterCounter("test_counter", "Test counter"); err != nil {
		t.Fatalf("failed to register counter: %v", err)
	}
	if err := registry.RegisterGauge("test_gauge", "Test gauge"); err != nil {
		t.Fatalf("failed to register gauge: %v", err)
	}
	if err := registry.RegisterHistogram("test_histogram", "Test histogram", []float64{1, 10, 100}); err != nil {
		t.Fatalf("failed to register histogram: %v", err)
	}

	// Record metrics
	if err := registry.Inc("test_counter"); err != nil {
		t.Fatalf("failed to increment counter: %v", err)
	}
	if err := registry.IncBy("test_counter", 5); err != nil {
		t.Fatalf("failed to increment counter by 5: %v", err)
	}
	if err := registry.Set("test_gauge", 42); err != nil {
		t.Fatalf("failed to set gauge: %v", err)
	}
	if err := registry.Add("test_gauge", 8); err != nil {
		t.Fatalf("failed to add to gauge: %v", err)
	}
	if err := registry.Observe("test_histogram", 50); err != nil {
		t.Fatalf("failed to observe histogram: %v", err)
	}

	// Get metrics
	metrics := registry.GetMetrics()
	if len(metrics) == 0 {
		t.Fatal("expected non-empty metrics")
	}

	// Check counter value
	found := false
	for _, m := range metrics {
		if m.Name == "test_counter" {
			if m.Value != 6 {
				t.Errorf("expected counter value to be 6, got %f", m.Value)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find test_counter metric")
	}

	// Check gauge value
	found = false
	for _, m := range metrics {
		if m.Name == "test_gauge" {
			if m.Value != 50 {
				t.Errorf("expected gauge value to be 50, got %f", m.Value)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find test_gauge metric")
	}
}

func TestDuplicateMetricRegistration(t *testing.T) {
	registry := newDefaultMetricsRegistry()

	if err := registry.RegisterCounter("test_counter", "Test"); err != nil {
		t.Fatalf("first registration should succeed: %v", err)
	}

	if err := registry.RegisterCounter("test_counter", "Test"); err == nil {
		t.Error("expected error for duplicate registration")
	}
}

func TestUnregisteredMetricOperations(t *testing.T) {
	registry := newDefaultMetricsRegistry()

	if err := registry.Inc("unregistered"); err == nil {
		t.Error("expected error for unregistered counter")
	}

	if err := registry.Set("unregistered", 1); err == nil {
		t.Error("expected error for unregistered gauge")
	}

	if err := registry.Observe("unregistered", 1); err == nil {
		t.Error("expected error for unregistered histogram")
	}
}

func TestPackageLevelMetricFunctions(t *testing.T) {
	// Register metrics using package-level functions
	if err := RegisterCounter("pkg_counter", "Package counter"); err != nil {
		t.Fatalf("failed to register counter: %v", err)
	}
	if err := RegisterGauge("pkg_gauge", "Package gauge"); err != nil {
		t.Fatalf("failed to register gauge: %v", err)
	}

	// Record metrics
	if err := Inc("pkg_counter"); err != nil {
		t.Fatalf("failed to increment: %v", err)
	}
	if err := Set("pkg_gauge", 100); err != nil {
		t.Fatalf("failed to set: %v", err)
	}

	// Get all metrics
	metrics := GetAllMetrics()
	if len(metrics) == 0 {
		t.Error("expected non-empty metrics")
	}
}

func TestBaggageEncoding(t *testing.T) {
	baggage := map[string]string{
		"key1": "value1",
		"key2": "value2",
	}

	encoded := encodeBaggage(baggage)
	if !strings.Contains(encoded, "key1=value1") {
		t.Error("expected encoded baggage to contain key1=value1")
	}
	if !strings.Contains(encoded, "key2=value2") {
		t.Error("expected encoded baggage to contain key2=value2")
	}

	// Test round-trip
	decoded := parseBaggage(encoded)
	if decoded["key1"] != "value1" {
		t.Errorf("expected decoded[key1]=value1, got %v", decoded["key1"])
	}
	if decoded["key2"] != "value2" {
		t.Errorf("expected decoded[key2]=value2, got %v", decoded["key2"])
	}
}

func TestSpanState(t *testing.T) {
	tests := []struct {
		state    SpanState
		expected string
	}{
		{SpanStateStarted, "started"},
		{SpanStateFinished, "finished"},
		{SpanStateErrored, "errored"},
		{SpanStatePaused, "paused"},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if string(tt.state) != tt.expected {
				t.Errorf("SpanState(%v).String() = %q, want %q", tt.state, string(tt.state), tt.expected)
			}
		})
	}
}

func TestMetricType(t *testing.T) {
	tests := []struct {
		mtype    MetricType
		expected string
	}{
		{MetricTypeCounter, "counter"},
		{MetricTypeGauge, "gauge"},
		{MetricTypeHistogram, "histogram"},
	}

	for _, tt := range tests {
		t.Run(string(tt.mtype), func(t *testing.T) {
			if string(tt.mtype) != tt.expected {
				t.Errorf("MetricType(%v).String() = %q, want %q", tt.mtype, string(tt.mtype), tt.expected)
			}
		})
	}
}

func TestLoggerConcurrency(t *testing.T) {
	logger := newDefaultLogger()
	logger.SetLevel(LevelDebug)
	logger.SetOutput(io.Discard)

	// Add a hook to count log entries
	var count int32
	logger.AddHook(func(level Level, msg string, fields Fields) {
		count++
	})

	// Log concurrently from multiple goroutines
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				logger.Info("concurrent message")
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	if count != 1000 {
		t.Errorf("expected 1000 log entries, got %d", count)
	}
}

func TestPackageLevelSpanFunctions(t *testing.T) {
	// Test StartSpan
	span := StartSpan("test-span")
	if span == nil {
		t.Fatal("expected non-nil span from StartSpan")
	}
	if span.Name != "test-span" {
		t.Errorf("expected span name to be test-span, got %q", span.Name)
	}

	// Test StartSpanWithContext
	ctx := context.Background()
	span2, ctx2 := StartSpanWithContext(ctx, "test-span-2")
	if span2 == nil {
		t.Fatal("expected non-nil span from StartSpanWithContext")
	}
	if TraceIDFromContext(ctx2) != span2.TraceID {
		t.Error("expected trace ID in context to match span")
	}

	// Test End
	End(span)
	if span.State != SpanStateFinished {
		t.Errorf("expected span state to be finished, got %q", span.State)
	}

	// Test EndWithError
	testErr := errors.New("test error")
	span3 := StartSpan("error-span")
	EndWithError(span3, testErr)
	if span3.State != SpanStateErrored {
		t.Errorf("expected span state to be errored, got %q", span3.State)
	}
}

func TestSetDefaultFunctions(t *testing.T) {
	// Test SetDefaultMetrics
	oldRegistry := globalMetricsRegistry
	defer func() {
		globalMetricsRegistry = oldRegistry
	}()

	newRegistry := newDefaultMetricsRegistry()
	SetDefaultMetrics(newRegistry)

	if DefaultMetrics() != newRegistry {
		t.Error("expected DefaultMetrics to return new registry")
	}
}

func TestPackageLevelMetricFunctionsAll(t *testing.T) {
	// Clean up any previous registrations by using a fresh registry
	oldRegistry := globalMetricsRegistry
	defer func() {
		globalMetricsRegistry = oldRegistry
	}()

	newRegistry := newDefaultMetricsRegistry()
	SetDefaultMetrics(newRegistry)

	// Register metrics
	if err := RegisterCounter("pkg_counter_all", "Package counter"); err != nil {
		t.Fatalf("failed to register counter: %v", err)
	}
	if err := RegisterGauge("pkg_gauge_all", "Package gauge"); err != nil {
		t.Fatalf("failed to register gauge: %v", err)
	}
	if err := RegisterHistogram("pkg_histogram_all", "Package histogram", []float64{1, 10, 100}); err != nil {
		t.Fatalf("failed to register histogram: %v", err)
	}

	// Test IncBy
	if err := IncBy("pkg_counter_all", 5); err != nil {
		t.Fatalf("failed to IncBy: %v", err)
	}

	// Test Add
	if err := Add("pkg_gauge_all", 10); err != nil {
		t.Fatalf("failed to Add: %v", err)
	}

	// Test Observe
	if err := Observe("pkg_histogram_all", 50); err != nil {
		t.Fatalf("failed to Observe: %v", err)
	}

	// Test Inc
	if err := Inc("pkg_counter_all"); err != nil {
		t.Fatalf("failed to Inc: %v", err)
	}

	// Test Set
	if err := Set("pkg_gauge_all", 100); err != nil {
		t.Fatalf("failed to Set: %v", err)
	}

	// Test GetAllMetrics
	metrics := GetAllMetrics()
	if len(metrics) == 0 {
		t.Error("expected non-empty metrics from GetAllMetrics")
	}
}

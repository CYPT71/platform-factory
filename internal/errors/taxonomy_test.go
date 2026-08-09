package errors

import (
	"encoding/json"
	"testing"
)

func TestRetryableClassifiesKnownCodes(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want bool
	}{
		{CodeTimeout, true},
		{CodeUnavailable, true},
		{CodeVMMUnavailable, true},
		{CodeCacheFull, true},
		{CodeConflict, false},
		{CodeInvalidArgument, false},
		{CodeInternal, false},
		{CodeCacheCorrupt, false},
	}
	for _, c := range cases {
		err := New(c.code, "boom")
		if got := Retryable(err); got != c.want {
			t.Errorf("Retryable(%s) = %v, want %v", c.code, got, c.want)
		}
	}
}

func TestRetryableDefaultsFalseForUnclassifiedOrUntyped(t *testing.T) {
	if Retryable(nil) {
		t.Error("Retryable(nil) should be false")
	}
	if Retryable(New(CodePluginProtocol, "x")) {
		t.Error("an unclassified typed code should default to not retryable")
	}
	untyped := errorString("plain error")
	if Retryable(untyped) {
		t.Error("an untyped error should default to not retryable")
	}
}

func TestExitCodeMapsKnownCodes(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want int
	}{
		{CodeInvalidArgument, 2},
		{CodeNotImplemented, 3},
		{CodePolicyViolation, 4},
		{CodeConflict, 5},
		{CodeUnavailable, 6},
		{CodeTimeout, 7},
		{CodeCacheCorrupt, 8},
		{CodeInternal, 10},
	}
	for _, c := range cases {
		err := New(c.code, "boom")
		if got := ExitCode(err); got != c.want {
			t.Errorf("ExitCode(%s) = %d, want %d", c.code, got, c.want)
		}
	}
}

func TestExitCodeDefaultsForNilUntypedAndUnclassified(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Errorf("ExitCode(nil) = %d, want 0", got)
	}
	if got := ExitCode(errorString("plain")); got != 1 {
		t.Errorf("ExitCode(untyped) = %d, want 1", got)
	}
	if got := ExitCode(New(CodePluginProtocol, "x")); got != 1 {
		t.Errorf("ExitCode(unclassified typed) = %d, want 1", got)
	}
}

func TestWithTraceIDRoundTripsThroughAllErrorKinds(t *testing.T) {
	base := New(CodeInvalidArgument, "bad input")
	withID := WithTraceID(base, "trace-123")
	if got := TraceID(withID); got != "trace-123" {
		t.Errorf("TraceID(base+trace) = %q, want trace-123", got)
	}
	if got := TraceID(base); got != "" {
		t.Errorf("original error should be unmodified, got TraceID=%q", got)
	}

	wrapped := Wrap(CodeInternal, "wrapping", base)
	wrappedWithID := WithTraceID(wrapped, "trace-456")
	if got := TraceID(wrappedWithID); got != "trace-456" {
		t.Errorf("TraceID(wrapped+trace) = %q, want trace-456", got)
	}

	multi := NewMultiError(CodePipelineValidation, []Issue{{Path: "a", Code: CodeInvalidArgument, Message: "x"}})
	multiWithID := WithTraceID(multi, "trace-789")
	if got := TraceID(multiWithID); got != "trace-789" {
		t.Errorf("TraceID(multi+trace) = %q, want trace-789", got)
	}

	if got := TraceID(errorString("plain")); got != "" {
		t.Errorf("TraceID(untyped) = %q, want empty", got)
	}
	if got := WithTraceID(errorString("plain"), "x"); got.Error() != "plain" {
		t.Errorf("WithTraceID(untyped) should return err unchanged, got %v", got)
	}
}

func TestMarshalJSONIncludesRetryableAndTraceID(t *testing.T) {
	err := WithTraceID(New(CodeTimeout, "slow"), "trace-abc")
	data, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var decoded struct {
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
		TraceID   string `json:"trace_id"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Code != string(CodeTimeout) || !decoded.Retryable || decoded.TraceID != "trace-abc" {
		t.Errorf("decoded=%+v", decoded)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

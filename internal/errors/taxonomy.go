package errors

import (
	"context"

	"github.com/CYPT71/secure-oci-base/internal/observability"
)

// This file adds the two pieces Sanetize-Stabilisation/Sanetizer-todo.md
// items 16-17 ask for that errors.go's existing typed-error model didn't
// yet have: a retryable/permanent classification per error code, and a
// stable process exit code per error code. trace_id (item 18) piggybacks
// on the same TraceID/WithTraceID pair rather than a new mechanism.
//
// New in this update: WithObservabilityTraceID for integrating with the
// observability package's TraceID type (item 18 propagation).
//
// Deliberately NOT done here: wiring ExitCode into every CLI command in
// cmd/platform-factory (dozens of handlers, each currently returning a
// hardcoded int) and propagating trace_id from CLI -> service -> plugin
// -> runtime -> evidence -> journal (item 18's full path) - both are
// real, separately-scoped efforts, not something to rush as part of
// defining the taxonomy itself.

// retryableByCode classifies each error code as transient (safe to retry
// the same operation unmodified) or permanent (retrying without changing
// something will fail the same way). Codes not listed default to
// permanent (false) via Retryable - unclassified errors should not be
// retried blindly.
var retryableByCode = map[ErrorCode]bool{
	CodeUnavailable:      true,
	CodeTimeout:          true,
	CodePipelineBudget:   false,
	CodeVMMUnavailable:   true,
	CodeVMMResource:      true,
	CodeCacheFull:        true,
	CodeCacheMiss:        false,
	CodeCacheCorrupt:     false,
	CodeCanceled:         false,
	CodeConflict:         false,
	CodeAlreadyExists:    false,
	CodeNotFound:         false,
	CodePermissionDenied: false,
	CodeInvalidArgument:  false,
	CodeInternal:         false,
	CodeNotImplemented:   false,
}

// Retryable reports whether err represents a condition where retrying the
// same operation unmodified might succeed. Untyped errors and typed
// errors with an unclassified code are treated as not retryable - the
// safe default when nothing says otherwise is "don't retry blindly."
func Retryable(err error) bool {
	code := GetErrorCode(err)
	if code == "" {
		return false
	}
	retryable, known := retryableByCode[code]
	return known && retryable
}

// exitCodeByCode maps each error code to the stable process exit code
// defined by Sanetizer-todo.md item 17. Codes not listed here fall back
// to 1 (ExitCode's default), the generic failure exit code this codebase
// already uses throughout cmd/platform-factory - these specific codes
// exist for scripts and CI to branch on a known failure category, not to
// replace 1 as the default for anything unclassified.
var exitCodeByCode = map[ErrorCode]int{
	CodeInvalidArgument:    2,
	CodeNotImplemented:     3,
	CodePolicyViolation:    4,
	CodePolicyEvaluation:   4,
	CodeConflict:           5,
	CodeAlreadyExists:      5,
	CodeRegistryConflict:   5,
	CodeUnavailable:        6,
	CodeVMMUnavailable:     6,
	CodePipelineDependency: 6,
	CodeTimeout:            7,
	CodeCacheCorrupt:       8,
	CodeInternal:           10,
}

// ExitCode maps err to the stable process exit code its typed error code
// corresponds to (0 for a nil err, 1 for an untyped or unclassified
// error - the generic failure this codebase already returns throughout
// cmd/platform-factory). Scripts and CI can rely on these numbers not
// changing meaning across releases.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	code := GetErrorCode(err)
	if code == "" {
		return 1
	}
	if exitCode, known := exitCodeByCode[code]; known {
		return exitCode
	}
	return 1
}

// TraceID returns the trace ID attached to err via WithTraceID, or "" if
// none was attached. This is item 18's per-error half of trace_id
// propagation - carrying an already-assigned trace ID through an error
// value, not assigning one in the first place.
func TraceID(err error) string {
	switch e := err.(type) {
	case *baseError:
		if e == nil {
			return ""
		}
		return e.traceID
	case *wrappedError:
		if e == nil {
			return ""
		}
		return e.traceID
	case *MultiError:
		if e == nil {
			return ""
		}
		return e.traceID
	default:
		return ""
	}
}

// WithTraceID returns a copy of err carrying traceID, without mutating
// err itself - callers that hold the original (e.g. via errors.Is
// comparisons elsewhere) are unaffected. Returns err unchanged if it
// isn't one of this package's own typed error types.
func WithTraceID(err error, traceID string) error {
	switch e := err.(type) {
	case *baseError:
		if e == nil {
			return err
		}
		copied := *e
		copied.traceID = traceID
		return &copied
	case *wrappedError:
		if e == nil {
			return err
		}
		copiedBase := *e.baseError
		copiedBase.traceID = traceID
		return &wrappedError{baseError: &copiedBase, wrapped: e.wrapped}
	case *MultiError:
		if e == nil {
			return err
		}
		copied := *e
		copied.traceID = traceID
		return &copied
	default:
		return err
	}
}

// WithObservabilityTraceID is a convenience wrapper around WithTraceID that accepts
// an observability.TraceID. This enables seamless integration with the
// observability package's trace ID generation.
//
// Example:
//
//	traceID := observability.NewTraceID("cli", "build")
//	err := someOperation()
//	if err != nil {
//	    return errors.WithObservabilityTraceID(err, traceID)
//	}
func WithObservabilityTraceID(err error, traceID observability.TraceID) error {
	return WithTraceID(err, string(traceID))
}

// TraceIDFromContext is a convenience wrapper around observability.TraceIDFromContext.
// It extracts the trace ID from the context as a string.
// Returns an empty string if no trace ID is present in the context or if ctx is nil.
func TraceIDFromContext(ctx context.Context) string {
	return observability.TraceIDFromContext(ctx)
}

// WithTraceIDFromContext returns a copy of err with the trace ID from ctx attached.
// This is a convenience for the common pattern:
//
//	WithTraceID(err, TraceIDFromContext(ctx))
//
// If ctx is nil or contains no trace ID, the error is returned unchanged.
func WithTraceIDFromContext(err error, ctx context.Context) error {
	if err == nil {
		return nil
	}
	traceID := TraceIDFromContext(ctx)
	if traceID == "" {
		return err
	}
	return WithTraceID(err, traceID)
}

// NewTraceID is a convenience wrapper around observability.NewTraceID.
// It generates a new trace ID with the given origin and command.
func NewTraceID(origin, command string) observability.TraceID {
	return observability.NewTraceID(origin, command)
}

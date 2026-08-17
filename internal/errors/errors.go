// Package errors provides a common typed error model for all platform-factory components.
// It enables consistent error handling, categorization, and introspection across
// the entire codebase while remaining compatible with the standard library error.
package errors

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ErrorCode represents a categorized error type that can be used for
// programmatic error handling across package boundaries.
type ErrorCode string

// Standard error codes used across the platform-factory project.
// Each code prefixes its domain (e.g., "pipeline.", "oci.", "plugin.").
const (
	// General errors
	CodeNotImplemented   ErrorCode = "not_implemented"
	CodeInvalidArgument  ErrorCode = "invalid_argument"
	CodeInternal         ErrorCode = "internal"
	CodeUnavailable      ErrorCode = "unavailable"
	CodeTimeout          ErrorCode = "timeout"
	CodeCanceled         ErrorCode = "canceled"
	CodePermissionDenied ErrorCode = "permission_denied"
	CodeNotFound         ErrorCode = "not_found"
	CodeAlreadyExists    ErrorCode = "already_exists"
	CodeConflict         ErrorCode = "conflict"

	// Pipeline-specific errors
	CodePipelineValidation ErrorCode = "pipeline.validation"
	CodePipelineCycle      ErrorCode = "pipeline.cycle"
	CodePipelineBudget     ErrorCode = "pipeline.budget_exceeded"
	CodePipelineDependency ErrorCode = "pipeline.dependency_failed"

	// OCI-specific errors
	CodeOCIValidation    ErrorCode = "oci.validation"
	CodeOCILayerError    ErrorCode = "oci.layer_error"
	CodeOCIPlatformError ErrorCode = "oci.platform_error"

	// Plugin-specific errors
	CodePluginProtocol   ErrorCode = "plugin.protocol"
	CodePluginSignature  ErrorCode = "plugin.signature_invalid"
	CodePluginCapability ErrorCode = "plugin.capability_missing"

	// Registry-specific errors
	CodeRegistryAuth     ErrorCode = "registry.auth_failed"
	CodeRegistryNotFound ErrorCode = "registry.not_found"
	CodeRegistryConflict ErrorCode = "registry.conflict"

	// VMM-specific errors
	CodeVMMUnavailable   ErrorCode = "vmm.unavailable"
	CodeVMMConfiguration ErrorCode = "vmm.configuration"
	CodeVMMResource      ErrorCode = "vmm.resource_error"

	// Cache-specific errors
	CodeCacheMiss    ErrorCode = "cache.miss"
	CodeCacheCorrupt ErrorCode = "cache.corrupt"
	CodeCacheFull    ErrorCode = "cache.full"

	// Policy-specific errors
	CodePolicyViolation  ErrorCode = "policy.violation"
	CodePolicyEvaluation ErrorCode = "policy.evaluation_failed"
)

// TypedError is the common error interface that all typed errors implement.
// It extends the standard error interface with code-based categorization.
type TypedError interface {
	error
	// Code returns the error category for programmatic handling.
	Code() ErrorCode
	// Details returns additional structured information about the error.
	// Returns nil if no details are available.
	Details() map[string]any
	// IsTyped returns true if the error implements TypedError.
	IsTyped() bool
}

// baseError is the concrete implementation of TypedError.
// All typed errors in this package embed or wrap a baseError.
type baseError struct {
	code    ErrorCode
	message string
	details map[string]any
	// traceID is set only via WithTraceID (see taxonomy.go); empty for
	// every error created through New/Newf/Wrap/etc. directly.
	traceID string
}

// Error implements the error interface.
func (e *baseError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString(string(e.code))
	if e.message != "" {
		b.WriteString(": ")
		b.WriteString(e.message)
	}
	if len(e.details) > 0 {
		b.WriteString(" [")
		first := true
		for k, v := range e.details {
			if !first {
				b.WriteString(", ")
			}
			first = false
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(fmt.Sprintf("%v", v))
		}
		b.WriteString("]")
	}
	return b.String()
}

// Code returns the error category.
func (e *baseError) Code() ErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// Details returns the structured details map.
func (e *baseError) Details() map[string]any {
	if e == nil {
		return nil
	}
	result := make(map[string]any, len(e.details))
	for k, v := range e.details {
		result[k] = v
	}
	return result
}

// IsTyped always returns true for baseError.
func (e *baseError) IsTyped() bool {
	return true
}

// MarshalJSON implements json.Marshaler for structured error serialization.
func (e *baseError) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Code      ErrorCode      `json:"code"`
		Message   string         `json:"message"`
		Retryable bool           `json:"retryable"`
		TraceID   string         `json:"trace_id,omitempty"`
		Details   map[string]any `json:"details,omitempty"`
	}{
		Code:      e.code,
		Message:   e.message,
		Retryable: Retryable(e),
		TraceID:   e.traceID,
		Details:   e.details,
	})
}

// newBaseError creates a new baseError with the given code and message.
func newBaseError(code ErrorCode, message string, details map[string]any) *baseError {
	return &baseError{
		code:    code,
		message: message,
		details: details,
	}
}

// New creates a new typed error with the given code and message.
// This is the primary entry point for creating typed errors.
func New(code ErrorCode, message string) error {
	return newBaseError(code, message, nil)
}

// Newf creates a new typed error with the given code and formatted message.
func Newf(code ErrorCode, format string, args ...any) error {
	return newBaseError(code, fmt.Sprintf(format, args...), nil)
}

// NewWithDetails creates a new typed error with code, message, and structured details.
func NewWithDetails(code ErrorCode, message string, details map[string]any) error {
	return newBaseError(code, message, details)
}

// NewWithDetailsf creates a new typed error with code, formatted message, and details.
func NewWithDetailsf(code ErrorCode, format string, details map[string]any, args ...any) error {
	return newBaseError(code, fmt.Sprintf(format, args...), details)
}

// Wrap wraps an existing error with a typed error code and message.
// The wrapped error can be retrieved using Unwrap or errors.Unwrap.
func Wrap(code ErrorCode, message string, err error) error {
	if err == nil {
		return nil
	}
	return &wrappedError{
		baseError: newBaseError(code, message, nil),
		wrapped:   err,
	}
}

// Wrapf wraps an existing error with a typed error code and formatted message.
func Wrapf(code ErrorCode, format string, err error, args ...any) error {
	if err == nil {
		return nil
	}
	return &wrappedError{
		baseError: newBaseError(code, fmt.Sprintf(format, args...), nil),
		wrapped:   err,
	}
}

// WrapWithDetails wraps an existing error with code, message, and details.
func WrapWithDetails(code ErrorCode, message string, err error, details map[string]any) error {
	if err == nil {
		return nil
	}
	return &wrappedError{
		baseError: newBaseError(code, message, details),
		wrapped:   err,
	}
}

// wrappedError wraps another error while adding typed error context.
type wrappedError struct {
	*baseError
	wrapped error
}

// Unwrap returns the wrapped error for use with errors.Is and errors.As.
func (e *wrappedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.wrapped
}

// Is implements the errors.Is interface for wrapped errors.
func (e *wrappedError) Is(target error) bool {
	if e == nil {
		return false
	}
	// Check if target is a TypedError with matching code
	if te, ok := target.(TypedError); ok {
		return e.code == te.Code()
	}
	// Check if target matches the wrapped error
	return fmt.Sprintf("%T", target) == fmt.Sprintf("%T", e.wrapped) ||
		(e.wrapped != nil && fmt.Sprintf("%v", e.wrapped) == fmt.Sprintf("%v", target))
}

// MarshalJSON implements json.Marshaler for wrapped errors.
func (e *wrappedError) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Code      ErrorCode      `json:"code"`
		Message   string         `json:"message"`
		Retryable bool           `json:"retryable"`
		TraceID   string         `json:"trace_id,omitempty"`
		Details   map[string]any `json:"details,omitempty"`
		Wrapped   string         `json:"wrapped,omitempty"`
	}{
		Code:      e.code,
		Message:   e.message,
		Retryable: Retryable(e),
		TraceID:   e.traceID,
		Details:   e.details,
		Wrapped:   e.wrapped.Error(),
	})
}

// GetTypedError attempts to extract a TypedError from an error.
// Returns nil if the error does not implement TypedError.
func GetTypedError(err error) TypedError {
	if err == nil {
		return nil
	}
	if te, ok := err.(TypedError); ok {
		return te
	}
	return nil
}

// GetErrorCode attempts to extract the error code from an error.
// Returns an empty string if the error does not implement TypedError.
func GetErrorCode(err error) ErrorCode {
	if te := GetTypedError(err); te != nil {
		return te.Code()
	}
	return ""
}

// HasCode checks if an error has the specified error code.
func HasCode(err error, code ErrorCode) bool {
	return GetErrorCode(err) == code
}

// HasAnyCode checks if an error has any of the specified error codes.
func HasAnyCode(err error, codes ...ErrorCode) bool {
	for _, c := range codes {
		if HasCode(err, c) {
			return true
		}
	}
	return false
}

// IsTyped checks if an error implements TypedError.
func IsTyped(err error) bool {
	_, ok := err.(TypedError)
	return ok
}

// Chain returns all errors in the error chain, from outermost to innermost.
// This is useful for collecting all error details for logging or reporting.
func Chain(err error) []error {
	if err == nil {
		return nil
	}
	var chain []error
	for err != nil {
		chain = append(chain, err)
		if unwrapped := unwrap(err); unwrapped != nil && unwrapped != err {
			err = unwrapped
		} else {
			break
		}
	}
	return chain
}

// unwrap is a helper to unwrap errors, handling both standard library
// and custom wrapped errors.
func unwrap(err error) error {
	if uw, ok := err.(interface{ Unwrap() error }); ok {
		return uw.Unwrap()
	}
	return nil
}

// RootCause returns the innermost error in the chain.
func RootCause(err error) error {
	chain := Chain(err)
	if len(chain) == 0 {
		return nil
	}
	return chain[len(chain)-1]
}

// FormatError formats an error with its chain for detailed reporting.
// Each error in the chain is indented according to its depth.
func FormatError(err error) string {
	if err == nil {
		return ""
	}
	var b strings.Builder
	chain := Chain(err)
	for i, e := range chain {
		if i > 0 {
			b.WriteString("\n  ")
		}
		b.WriteString(e.Error())
		// Add code if it's a typed error
		if te := GetTypedError(e); te != nil {
			b.WriteString(fmt.Sprintf(" [code=%s]", te.Code()))
		}
	}
	return b.String()
}

// Issue represents a single validation or processing issue, suitable for
// collecting multiple problems before returning them as a single error.
type Issue struct {
	Path    string    `json:"path"`
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// MultiError aggregates multiple issues into a single error.
type MultiError struct {
	code   ErrorCode
	Issues []Issue `json:"issues"`
	// traceID is set only via WithTraceID (see taxonomy.go).
	traceID string
}

// Error implements the error interface.
func (e *MultiError) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "multiple errors occurred"
	}
	var b strings.Builder
	b.WriteString(string(e.code))
	b.WriteString(": ")
	b.WriteString(fmt.Sprintf("%d issue(s)", len(e.Issues)))
	for _, issue := range e.Issues {
		b.WriteString(fmt.Sprintf("\n  - %s: %s [code=%s]", issue.Path, issue.Message, issue.Code))
	}
	return b.String()
}

// Code returns the error category.
func (e *MultiError) Code() ErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// Details returns the issues as structured details.
func (e *MultiError) Details() map[string]any {
	if e == nil {
		return nil
	}
	issues := make([]map[string]any, len(e.Issues))
	for i, issue := range e.Issues {
		issues[i] = map[string]any{
			"path":    issue.Path,
			"code":    issue.Code,
			"message": issue.Message,
		}
	}
	return map[string]any{
		"issues": issues,
	}
}

// IsTyped always returns true for MultiError.
func (e *MultiError) IsTyped() bool {
	return true
}

// MarshalJSON implements json.Marshaler for structured error serialization.
func (e *MultiError) MarshalJSON() ([]byte, error) {
	issues := make([]map[string]any, len(e.Issues))
	for i, issue := range e.Issues {
		issues[i] = map[string]any{
			"path":    issue.Path,
			"code":    issue.Code,
			"message": issue.Message,
		}
	}
	payload := map[string]any{
		"code":      e.code,
		"issues":    issues,
		"retryable": Retryable(e),
	}
	if e.traceID != "" {
		payload["trace_id"] = e.traceID
	}
	return json.Marshal(payload)
}

// Unwrap returns the first issue as an error if there are issues, nil otherwise.
// This allows MultiError to work with errors.Is and errors.As for the first issue.
func (e *MultiError) Unwrap() error {
	if e == nil || len(e.Issues) == 0 {
		return nil
	}
	return NewWithDetails(e.Issues[0].Code, e.Issues[0].Message, map[string]any{
		"path": e.Issues[0].Path,
	})
}

// NewMultiError creates a new MultiError with the given code and issues.
func NewMultiError(code ErrorCode, issues []Issue) error {
	if len(issues) == 0 {
		return New(code, "no issues reported")
	}
	return &MultiError{
		code:   code,
		Issues: issues,
	}
}

// AppendIssue adds an issue to a MultiError, creating one if necessary.
// If err is nil, a new MultiError is created with the single issue.
// If err is already a MultiError with the same code, the issue is appended.
// Otherwise, the issue is wrapped with the existing error.
func AppendIssue(err error, code ErrorCode, path, message string) error {
	if err == nil {
		return NewMultiError(code, []Issue{{Path: path, Code: code, Message: message}})
	}

	// Check if it's already a MultiError with matching code
	if me, ok := err.(*MultiError); ok && me.code == code {
		me.Issues = append(me.Issues, Issue{Path: path, Code: code, Message: message})
		return me
	}

	// Wrap the existing error with a new MultiError
	return NewMultiError(code, []Issue{{Path: path, Code: code, Message: message}})
}

// IsMultiError checks if an error is a MultiError.
func IsMultiError(err error) bool {
	_, ok := err.(*MultiError)
	return ok
}

// GetMultiError attempts to extract a MultiError from an error.
func GetMultiError(err error) *MultiError {
	if me, ok := err.(*MultiError); ok {
		return me
	}
	return nil
}

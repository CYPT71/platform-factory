package errors

import (
	"encoding/json"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
)

func TestNewError(t *testing.T) {
	err := New(CodeInvalidArgument, "invalid input")
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	typed, ok := err.(TypedError)
	if !ok {
		t.Fatal("expected TypedError")
	}
	if typed.Code() != CodeInvalidArgument {
		t.Errorf("expected code %s, got %s", CodeInvalidArgument, typed.Code())
	}
	if typed.IsTyped() != true {
		t.Error("expected IsTyped() to return true")
	}
	if len(typed.Details()) != 0 {
		t.Errorf("expected empty details, got %v", typed.Details())
	}

	if err.Error() != "invalid_argument: invalid input" {
		t.Errorf("unexpected error string: %s", err.Error())
	}
}

func TestNewfError(t *testing.T) {
	err := Newf(CodeNotFound, "resource %s not found", "test-resource")
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	typed := GetTypedError(err)
	if typed == nil {
		t.Fatal("expected TypedError")
	}
	if typed.Code() != CodeNotFound {
		t.Errorf("expected code %s, got %s", CodeNotFound, typed.Code())
	}

	expectedMsg := "resource test-resource not found"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error to contain %q, got %q", expectedMsg, err.Error())
	}
}

func TestNewWithDetails(t *testing.T) {
	err := NewWithDetails(CodeInternal, "operation failed", map[string]any{
		"operation": "test-op",
		"count":     42,
	})

	typed := GetTypedError(err)
	if typed == nil {
		t.Fatal("expected TypedError")
	}

	details := typed.Details()
	if details["operation"] != "test-op" {
		t.Errorf("expected operation=test-op, got %v", details["operation"])
	}
	if details["count"] != 42 {
		t.Errorf("expected count=42, got %v", details["count"])
	}

	// Check that details appear in Error() string
	errStr := err.Error()
	if !strings.Contains(errStr, "operation=test-op") || !strings.Contains(errStr, "count=42") {
		t.Errorf("expected details in error string, got: %s", errStr)
	}
}

func TestNewWithDetailsf(t *testing.T) {
	err := NewWithDetailsf(CodeInternal, "operation %s failed with %d errors", map[string]any{
		"operation": "test-op",
	}, "test-op", 5)

	typed := GetTypedError(err)
	if typed == nil {
		t.Fatal("expected TypedError")
	}
	if typed.Code() != CodeInternal {
		t.Errorf("expected code %s, got %s", CodeInternal, typed.Code())
	}

	// Check formatted message
	errStr := err.Error()
	if !strings.Contains(errStr, "operation test-op failed with 5 errors") {
		t.Errorf("expected formatted message in error string, got: %s", errStr)
	}

	// Check details
	details := typed.Details()
	if details["operation"] != "test-op" {
		t.Errorf("expected operation=test-op in details, got %v", details["operation"])
	}
}

func TestWrapError(t *testing.T) {
	baseErr := stderrors.New("base error")
	wrapped := Wrap(CodePipelineValidation, "pipeline validation failed", baseErr)

	if wrapped == nil {
		t.Fatal("expected non-nil error")
	}

	typed := GetTypedError(wrapped)
	if typed == nil {
		t.Fatal("expected TypedError")
	}
	if typed.Code() != CodePipelineValidation {
		t.Errorf("expected code %s, got %s", CodePipelineValidation, typed.Code())
	}

	// Check unwrap
	if !stderrors.Is(wrapped, baseErr) {
		t.Error("expected wrapped error to be unwrappable")
	}
}

func TestWrapfError(t *testing.T) {
	baseErr := stderrors.New("io error")
	wrapped := Wrapf(CodeRegistryAuth, "authentication to %s failed", baseErr, "ghcr.io")

	typed := GetTypedError(wrapped)
	if typed == nil {
		t.Fatal("expected TypedError")
	}
	if typed.Code() != CodeRegistryAuth {
		t.Errorf("expected code %s, got %s", CodeRegistryAuth, typed.Code())
	}

	if !stderrors.Is(wrapped, baseErr) {
		t.Error("expected wrapped error to be unwrappable")
	}
}

func TestWrapWithDetails(t *testing.T) {
	baseErr := stderrors.New("underlying error")
	wrapped := WrapWithDetails(CodeVMMUnavailable, "VMM not available", baseErr, map[string]any{
		"capability": "kvm",
	})

	typed := GetTypedError(wrapped)
	if typed == nil {
		t.Fatal("expected TypedError")
	}

	details := typed.Details()
	if details["capability"] != "kvm" {
		t.Errorf("expected capability=kvm, got %v", details["capability"])
	}

	if !stderrors.Is(wrapped, baseErr) {
		t.Error("expected wrapped error to be unwrappable")
	}
}

func TestWrapNilError(t *testing.T) {
	// Wrapping nil should return nil
	if err := Wrap(CodeInternal, "message", nil); err != nil {
		t.Error("wrapping nil should return nil")
	}
	if err := Wrapf(CodeInternal, "message", nil); err != nil {
		t.Error("wrapping nil should return nil")
	}
	if err := WrapWithDetails(CodeInternal, "message", nil, nil); err != nil {
		t.Error("wrapping nil should return nil")
	}
}

func TestGetErrorCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected ErrorCode
	}{
		{"typed error", New(CodeNotFound, "not found"), CodeNotFound},
		{"wrapped typed error", Wrap(CodeInternal, "wrapped", New(CodeNotFound, "not found")), CodeInternal},
		{"standard error", stderrors.New("standard error"), ""},
		{"nil error", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetErrorCode(tt.err)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestHasCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code ErrorCode
		want bool
	}{
		{"matching code", New(CodeNotFound, "msg"), CodeNotFound, true},
		{"different code", New(CodeNotFound, "msg"), CodeInternal, false},
		{"standard error", stderrors.New("err"), CodeNotFound, false},
		{"nil error", nil, CodeNotFound, false},
		{"wrapped error", Wrap(CodeInternal, "wrapped", New(CodeNotFound, "msg")), CodeInternal, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasCode(tt.err, tt.code)
			if got != tt.want {
				t.Errorf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestHasAnyCode(t *testing.T) {
	err := New(CodeNotFound, "msg")

	if !HasAnyCode(err, CodeNotFound, CodeInternal) {
		t.Error("expected HasAnyCode to return true for CodeNotFound")
	}
	if HasAnyCode(err, CodeInternal, CodeTimeout) {
		t.Error("expected HasAnyCode to return false for non-matching codes")
	}
}

func TestIsTyped(t *testing.T) {
	if !IsTyped(New(CodeInternal, "msg")) {
		t.Error("expected typed error to return true for IsTyped")
	}
	if IsTyped(stderrors.New("standard error")) {
		t.Error("expected standard error to return false for IsTyped")
	}
}

func TestChain(t *testing.T) {
	inner := stderrors.New("inner")
	middle := fmt.Errorf("middle: %w", inner)
	outer := Wrap(CodeInternal, "outer", middle)

	chain := Chain(outer)
	if len(chain) != 3 {
		t.Errorf("expected chain length 3, got %d", len(chain))
	}

	// Check that chain contains all errors
	found := false
	for _, e := range chain {
		if e.Error() == "inner" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find inner error in chain")
	}
}

func TestRootCause(t *testing.T) {
	inner := stderrors.New("root cause")
	middle := fmt.Errorf("middle: %w", inner)
	outer := Wrap(CodeInternal, "outer", middle)

	root := RootCause(outer)
	if root != inner {
		t.Errorf("expected root cause to be %v, got %v", inner, root)
	}
}

func TestFormatError(t *testing.T) {
	inner := New(CodeNotFound, "not found")
	outer := Wrap(CodeInternal, "wrapped", inner)

	formatted := FormatError(outer)
	if !strings.Contains(formatted, "internal") {
		t.Error("expected formatted error to contain 'internal'")
	}
	if !strings.Contains(formatted, "not_found") {
		t.Error("expected formatted error to contain 'not_found'")
	}
	if !strings.Contains(formatted, "code=") {
		t.Error("expected formatted error to contain error codes")
	}
}

func TestMultiError(t *testing.T) {
	issues := []Issue{
		{Path: "field1", Code: CodeInvalidArgument, Message: "field1 is invalid"},
		{Path: "field2", Code: CodeInvalidArgument, Message: "field2 is required"},
	}

	err := NewMultiError(CodePipelineValidation, issues)
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	typed := GetTypedError(err)
	if typed == nil {
		t.Fatal("expected TypedError")
	}
	if typed.Code() != CodePipelineValidation {
		t.Errorf("expected code %s, got %s", CodePipelineValidation, typed.Code())
	}

	// Test Details() method
	details := typed.Details()
	if details == nil {
		t.Fatal("expected non-nil details")
	}
	if details["issues"] == nil {
		t.Error("expected issues in details")
	}

	// Test IsTyped() method
	if !typed.IsTyped() {
		t.Error("expected IsTyped() to return true")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "2 issue(s)") {
		t.Errorf("expected error to mention 2 issues, got: %s", errStr)
	}
	if !strings.Contains(errStr, "field1 is invalid") {
		t.Errorf("expected error to contain field1 message, got: %s", errStr)
	}
	if !strings.Contains(errStr, "field2 is required") {
		t.Errorf("expected error to contain field2 message, got: %s", errStr)
	}
}

func TestMultiErrorCodeMethod(t *testing.T) {
	// Test MultiError.Code() method directly
	issues := []Issue{
		{Path: "test", Code: CodeInternal, Message: "error"},
	}
	err := NewMultiError(CodeNotFound, issues)
	me := GetMultiError(err)
	if me == nil {
		t.Fatal("expected MultiError")
	}
	if me.Code() != CodeNotFound {
		t.Errorf("expected code %s, got %s", CodeNotFound, me.Code())
	}
}

func TestAppendIssue(t *testing.T) {
	// Start with nil
	err := AppendIssue(nil, CodeInvalidArgument, "field1", "invalid")
	if !IsMultiError(err) {
		t.Error("expected MultiError after first AppendIssue")
	}

	// Append to existing MultiError with same code
	err = AppendIssue(err, CodeInvalidArgument, "field2", "also invalid")
	me := GetMultiError(err)
	if me == nil || len(me.Issues) != 2 {
		t.Errorf("expected 2 issues, got %d", len(me.Issues))
	}

	// Append to existing MultiError with different code creates new error
	err = AppendIssue(err, CodeNotFound, "field3", "not found")
	if !IsMultiError(err) {
		t.Error("expected MultiError")
	}
}

func TestMultiErrorJSON(t *testing.T) {
	issues := []Issue{
		{Path: "test", Code: CodeInternal, Message: "error message"},
	}
	err := NewMultiError(CodePipelineValidation, issues)

	data, jsonErr := json.Marshal(err)
	if jsonErr != nil {
		t.Fatalf("failed to marshal: %v", jsonErr)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if result["code"] != string(CodePipelineValidation) {
		t.Errorf("expected code %s in JSON, got %v", CodePipelineValidation, result["code"])
	}
}

func TestWrappedErrorJSON(t *testing.T) {
	inner := stderrors.New("inner error")
	err := WrapWithDetails(CodeInternal, "wrapped", inner, map[string]any{"key": "value"})

	data, jsonErr := json.Marshal(err)
	if jsonErr != nil {
		t.Fatalf("failed to marshal: %v", jsonErr)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if result["code"] != string(CodeInternal) {
		t.Errorf("expected code %s in JSON, got %v", CodeInternal, result["code"])
	}
	if result["wrapped"] != "inner error" {
		t.Errorf("expected wrapped error in JSON, got %v", result["wrapped"])
	}
}

func TestBaseErrorJSON(t *testing.T) {
	err := NewWithDetails(CodeNotFound, "resource missing", map[string]any{"resource": "test"})

	data, jsonErr := json.Marshal(err)
	if jsonErr != nil {
		t.Fatalf("failed to marshal: %v", jsonErr)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if result["code"] != string(CodeNotFound) {
		t.Errorf("expected code %s in JSON, got %v", CodeNotFound, result["code"])
	}
	if result["message"] != "resource missing" {
		t.Errorf("expected message in JSON, got %v", result["message"])
	}
	if result["details"] == nil {
		t.Error("expected details in JSON")
	}
}

func TestNilErrorHandling(t *testing.T) {
	// All functions should handle nil gracefully
	if GetTypedError(nil) != nil {
		t.Error("GetTypedError(nil) should return nil")
	}
	if GetErrorCode(nil) != "" {
		t.Error("GetErrorCode(nil) should return empty string")
	}
	if HasCode(nil, CodeInternal) {
		t.Error("HasCode(nil, ...) should return false")
	}
	if IsTyped(nil) {
		t.Error("IsTyped(nil) should return false")
	}
	if Chain(nil) != nil {
		t.Error("Chain(nil) should return nil")
	}
	if RootCause(nil) != nil {
		t.Error("RootCause(nil) should return nil")
	}
	if FormatError(nil) != "" {
		t.Error("FormatError(nil) should return empty string")
	}
}

func TestErrorCodesAreUnique(t *testing.T) {
	// Ensure all error codes are unique
	codes := []ErrorCode{
		CodeNotImplemented,
		CodeInvalidArgument,
		CodeInternal,
		CodeUnavailable,
		CodeTimeout,
		CodeCanceled,
		CodePermissionDenied,
		CodeNotFound,
		CodeAlreadyExists,
		CodeConflict,
		CodePipelineValidation,
		CodePipelineCycle,
		CodePipelineBudget,
		CodePipelineDependency,
		CodeOCIValidation,
		CodeOCILayerError,
		CodeOCIPlatformError,
		CodePluginProtocol,
		CodePluginSignature,
		CodePluginCapability,
		CodeRegistryAuth,
		CodeRegistryNotFound,
		CodeRegistryConflict,
		CodeVMMUnavailable,
		CodeVMMConfiguration,
		CodeVMMResource,
		CodeCacheMiss,
		CodeCacheCorrupt,
		CodeCacheFull,
		CodePolicyViolation,
		CodePolicyEvaluation,
	}

	seen := make(map[ErrorCode]bool)
	for _, code := range codes {
		if seen[code] {
			t.Errorf("duplicate error code: %s", code)
		}
		seen[code] = true
	}
}

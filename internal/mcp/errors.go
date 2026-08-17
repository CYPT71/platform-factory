package mcp

import "fmt"

// toolError is a structured, user-facing error a tool handler returns
// when the request itself is bad or the operation cannot complete for a
// known reason. It is rendered as MCP tool-result content with
// isError=true (per the MCP spec, tool-level failures are NOT JSON-RPC
// protocol errors - the call succeeded, the requested operation failed),
// never as a bare Go error string that might leak an internal path or
// stack trace.
type toolError struct {
	// Code is a short, stable, machine-matchable identifier such as
	// "invalid_argument", "path_outside_repo", or "branch_protected".
	Code string
	// Message is safe to show to the calling model: no absolute host
	// paths, no secret values, no raw subprocess output beyond what a
	// caller needs to fix its request.
	Message string
}

func (e *toolError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func newToolError(code, format string, args ...any) *toolError {
	return &toolError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Common tool error codes shared across the project/plugins/core/git
// subpackages, kept here so callers compare against one source of truth
// instead of re-declaring string literals.
const (
	ErrInvalidArgument  = "invalid_argument"
	ErrNotFound         = "not_found"
	ErrPathOutsideRepo  = "path_outside_repo"
	ErrBranchProtected  = "branch_protected"
	ErrDirtyWorkingTree = "dirty_working_tree"
	ErrValidationFailed = "validation_failed"
	ErrAgentUnavailable = "agent_unavailable"
	ErrInternal         = "internal"
)

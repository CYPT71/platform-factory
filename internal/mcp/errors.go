package mcp

import "github.com/CYPT71/platform-factory/internal/mcp/toolerror"

// toolError is a structured, user-facing error a tool handler returns
// when the request itself is bad or the operation cannot complete for a
// known reason. It is rendered as MCP tool-result content with
// isError=true (per the MCP spec, tool-level failures are NOT JSON-RPC
// protocol errors - the call succeeded, the requested operation failed),
// never as a bare Go error string that might leak an internal path or
// stack trace.
//
// The type itself lives in internal/mcp/toolerror, a leaf package with
// no dependents among the internal/mcp subpackages (core, product,
// plugins, git, project, agent). Those subpackages construct
// *toolerror.ToolError values directly via toolerror.New so they never
// need to import this package back (see the doc comment on
// NewPlatformFactoryServer in register.go). toolError is aliased here
// purely so this package's own code and tests can keep referring to the
// short, unexported name.
type toolError = toolerror.ToolError

func newToolError(code, format string, args ...any) *toolError {
	return toolerror.New(code, format, args...)
}

// Common tool error codes shared across the project/plugins/core/git
// subpackages, kept here so callers compare against one source of truth
// instead of re-declaring string literals.
const (
	ErrInvalidArgument  = toolerror.ErrInvalidArgument
	ErrNotFound         = toolerror.ErrNotFound
	ErrPathOutsideRepo  = toolerror.ErrPathOutsideRepo
	ErrBranchProtected  = toolerror.ErrBranchProtected
	ErrDirtyWorkingTree = toolerror.ErrDirtyWorkingTree
	ErrValidationFailed = toolerror.ErrValidationFailed
	ErrAgentUnavailable = toolerror.ErrAgentUnavailable
	ErrAlreadyExists    = toolerror.ErrAlreadyExists
	ErrConflict         = toolerror.ErrConflict
	ErrInternal         = toolerror.ErrInternal
)

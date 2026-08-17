// Package mcp implements a native Model Context Protocol server for
// platform-factory: a stdio JSON-RPC transport (protocol.go, server.go),
// a tool/resource registry (tools.go, resources.go), and typed error
// mapping (errors.go), with the actual platform-factory-specific tools
// implemented in the project/, plugins/, core/, git/, and agent/
// subpackages. Only the MCP protocol subset this server actually needs
// is implemented (initialize, tools/list, tools/call, resources/list,
// resources/read, ping) - not a general-purpose MCP SDK.
package mcp

import "encoding/json"

// protocolVersion is the MCP protocol date-version this server speaks.
const protocolVersion = "2025-06-18"

// jsonrpcVersion is the fixed JSON-RPC 2.0 version string every message
// carries.
const jsonrpcVersion = "2.0"

// Standard JSON-RPC 2.0 error codes (https://www.jsonrpc.org/specification#error_object).
const (
	errCodeParseError     = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeInternalError  = -32603
)

// request is one JSON-RPC 2.0 request or notification read from stdin.
// A notification has no ID and must never receive a response.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (r request) isNotification() bool { return len(r.ID) == 0 }

// response is one JSON-RPC 2.0 response written to stdout. Exactly one
// of Result/Error is set.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// initializeParams is the client's opening handshake payload.
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      clientInfo     `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeResult is the server's handshake response.
type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      serverInfo     `json:"serverInfo"`
	Instructions    string         `json:"instructions,omitempty"`
}

// toolDescriptor is one entry in a tools/list response.
type toolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []toolDescriptor `json:"tools"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// contentBlock is one piece of a tool result or resource read - only the
// "text" content type is produced by this server; every tool result is
// human/LLM-readable structured text (usually JSON), never binary.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// resourceDescriptor is one entry in a resources/list response.
type resourceDescriptor struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type resourcesListResult struct {
	Resources []resourceDescriptor `json:"resources"`
}

type resourceReadParams struct {
	URI string `json:"uri"`
}

type resourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

type resourceReadResult struct {
	Contents []resourceContents `json:"contents"`
}

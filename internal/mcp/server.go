package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
)

// maxLineBytes bounds a single incoming JSON-RPC message. MCP messages
// are small (tool arguments, not file payloads - tools that need to
// return large content write it through a resource or a bounded summary
// instead), so a generous but finite cap catches a malformed or hostile
// client instead of growing memory without limit.
const maxLineBytes = 16 << 20 // 16 MiB

// Server is a stdio MCP server: it owns a tool registry, a resource
// registry, and the JSON-RPC read/dispatch/write loop. All diagnostic
// logging goes to the stderr writer given to Serve - stdout carries only
// well-formed MCP protocol messages, per the transport's requirement
// that a client can safely treat stdout as machine-readable.
type Server struct {
	Name    string
	Version string

	tools     *toolRegistry
	resources *resourceRegistry
}

// NewServer constructs an empty server. Call AddTool/AddResource to
// register capabilities, then Serve to run the stdio loop.
func NewServer(name, version string) *Server {
	return &Server{
		Name:      name,
		Version:   version,
		tools:     newToolRegistry(),
		resources: newResourceRegistry(),
	}
}

func (s *Server) AddTool(t Tool)         { s.tools.Add(t) }
func (s *Server) AddResource(r Resource) { s.resources.Add(r) }

// Serve runs the read-dispatch-write loop until stdin is closed or ctx
// is canceled. Each line read from stdin is one JSON-RPC 2.0 message;
// each response is written as one JSON-RPC 2.0 message followed by a
// newline. Requests are handled sequentially, in the order received -
// MCP stdio clients send one request and await its response before
// sending the next in practice, and sequential handling keeps this
// server's own file/git operations from racing each other.
func (s *Server) Serve(ctx context.Context, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	logger := log.New(stderr, "pf-mcp: ", log.LstdFlags)

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	enc := json.NewEncoder(stdout)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}

		line := scanner.Bytes()
		if len(bytesTrimSpace(line)) == 0 {
			continue // blank lines between messages are harmless
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			logger.Printf("parse error: %v", err)
			s.writeResponse(enc, logger, response{
				JSONRPC: jsonrpcVersion,
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: errCodeParseError, Message: "invalid JSON"},
			})
			continue
		}

		resp := s.dispatch(ctx, logger, req)
		if resp == nil {
			continue // notification: no response is ever sent
		}
		s.writeResponse(enc, logger, *resp)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("mcp: reading stdin: %w", err)
	}
	return nil
}

func (s *Server) writeResponse(enc *json.Encoder, logger *log.Logger, resp response) {
	if err := enc.Encode(resp); err != nil {
		logger.Printf("write error: %v", err)
	}
}

// dispatch handles one decoded request and returns the response to
// write, or nil if req was a notification (which never gets a
// response).
func (s *Server) dispatch(ctx context.Context, logger *log.Logger, req request) *response {
	if req.JSONRPC != jsonrpcVersion {
		return s.errorResponse(req, errCodeInvalidRequest, "jsonrpc must be \"2.0\"")
	}

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized", "notifications/cancelled":
		return nil // acknowledged implicitly; nothing to do
	case "ping":
		return s.result(req, map[string]any{})
	case "tools/list":
		return s.result(req, toolsListResult{Tools: s.tools.list()})
	case "tools/call":
		return s.handleToolsCall(ctx, logger, req)
	case "resources/list":
		return s.result(req, resourcesListResult{Resources: s.resources.list()})
	case "resources/read":
		return s.handleResourcesRead(ctx, req)
	default:
		if req.isNotification() {
			return nil // unknown notifications are silently ignored, per spec
		}
		return s.errorResponse(req, errCodeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method))
	}
}

func (s *Server) handleInitialize(req request) *response {
	var params initializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.errorResponse(req, errCodeInvalidParams, "invalid initialize params")
		}
	}
	return s.result(req, initializeResult{
		ProtocolVersion: protocolVersion,
		Capabilities: map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
		},
		ServerInfo: serverInfo{Name: s.Name, Version: s.Version},
		Instructions: "Platform Factory MCP server. Use tools/list and resources/list to " +
			"discover available operations. Mutating tools never push to or merge into " +
			"main; core changes are proposed as a draft pull request for human review.",
	})
}

func (s *Server) handleToolsCall(ctx context.Context, logger *log.Logger, req request) *response {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.errorResponse(req, errCodeInvalidParams, "invalid tools/call params")
	}
	tool, ok := s.tools.get(params.Name)
	if !ok {
		return s.errorResponse(req, errCodeInvalidParams, fmt.Sprintf("unknown tool %q", params.Name))
	}

	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	text, err := tool.Handler(ctx, args)
	if err != nil {
		var te *toolError
		if errors.As(err, &te) {
			return s.result(req, toolCallResult{
				Content: []contentBlock{{Type: "text", Text: te.Error()}},
				IsError: true,
			})
		}
		logger.Printf("tool %q internal error: %v", params.Name, err)
		return s.result(req, toolCallResult{
			Content: []contentBlock{{Type: "text", Text: fmt.Sprintf("%s: internal error", ErrInternal)}},
			IsError: true,
		})
	}
	return s.result(req, toolCallResult{Content: []contentBlock{{Type: "text", Text: text}}})
}

func (s *Server) handleResourcesRead(ctx context.Context, req request) *response {
	var params resourceReadParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.errorResponse(req, errCodeInvalidParams, "invalid resources/read params")
	}
	res, ok := s.resources.get(params.URI)
	if !ok {
		return s.errorResponse(req, errCodeInvalidParams, fmt.Sprintf("unknown resource %q", params.URI))
	}
	text, mimeType, err := res.Handler(ctx)
	if err != nil {
		return s.errorResponse(req, errCodeInternalError, fmt.Sprintf("resource %q unavailable", params.URI))
	}
	if mimeType == "" {
		mimeType = res.MimeType
	}
	return s.result(req, resourceReadResult{
		Contents: []resourceContents{{URI: res.URI, MimeType: mimeType, Text: text}},
	})
}

func (s *Server) result(req request, result any) *response {
	if req.isNotification() {
		return nil
	}
	return &response{JSONRPC: jsonrpcVersion, ID: req.ID, Result: result}
}

func (s *Server) errorResponse(req request, code int, message string) *response {
	if req.isNotification() {
		return nil
	}
	id := req.ID
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return &response{JSONRPC: jsonrpcVersion, ID: id, Error: &rpcError{Code: code, Message: message}}
}

func bytesTrimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isASCIISpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isASCIISpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

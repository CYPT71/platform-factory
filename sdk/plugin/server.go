package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Handler implements one plugin capability.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server is the plugin-side SDK: register capabilities, then Serve.
type Server struct {
	name, version string
	capabilities  []string
	handlers      map[string]Handler
}

// NewServer returns a Server that will report name and version during the
// v1.hello handshake.
func NewServer(name, version string) *Server {
	return &Server{name: name, version: version, handlers: map[string]Handler{}}
}

// Handle registers handler for capability (e.g. "detect"), dispatched on
// method "v1."+capability and advertised in the v1.hello response.
func (s *Server) Handle(capability string, handler Handler) {
	s.capabilities = append(s.capabilities, capability)
	s.handlers["v1."+capability] = handler
}

// Serve reads framed requests from r and writes framed responses to w
// until r is exhausted (the host closed the connection) or ctx is
// canceled. It returns nil on a clean shutdown.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	reader := bufio.NewReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		raw, err := ReadMessage(reader)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("plugin: serve: %w", err)
		}
		var req Request
		if err := json.Unmarshal(raw, &req); err != nil {
			return fmt.Errorf("plugin: serve: decode request: %w", err)
		}
		if err := WriteMessage(w, s.dispatch(ctx, req)); err != nil {
			return fmt.Errorf("plugin: serve: write response: %w", err)
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req Request) Response {
	if req.Method == "v1.hello" {
		result, err := json.Marshal(HelloResult{
			APIVersion: ProtocolVersion, Name: s.name, Version: s.version, Capabilities: s.capabilities,
		})
		if err != nil {
			return Response{ID: req.ID, Error: &RPCError{Code: 500, Message: err.Error()}}
		}
		return Response{ID: req.ID, Result: result, TraceID: req.TraceID, OperationID: req.OperationID}
	}

	handler, ok := s.handlers[req.Method]
	if !ok {
		return Response{ID: req.ID, Error: &RPCError{Code: 404, Message: fmt.Sprintf("unknown method %q", req.Method)}, TraceID: req.TraceID, OperationID: req.OperationID}
	}
	value, err := handler(contextWithRequestIDs(ctx, req.TraceID, req.OperationID), req.Params)
	if err != nil {
		return Response{ID: req.ID, Error: &RPCError{Code: 500, Message: err.Error()}, TraceID: req.TraceID, OperationID: req.OperationID}
	}
	result, err := json.Marshal(value)
	if err != nil {
		return Response{ID: req.ID, Error: &RPCError{Code: 500, Message: "encode result: " + err.Error()}, TraceID: req.TraceID, OperationID: req.OperationID}
	}
	return Response{ID: req.ID, Result: result, TraceID: req.TraceID, OperationID: req.OperationID}
}

// Package plugin is the public SDK for out-of-process secure-oci
// plugins. It defines the versioned, length-prefixed JSON-RPC protocol
// plugins speak over stdin/stdout (an LSP/DAP-style header-framed
// message on the wire), the plugin-side Server, the handshake shape and
// the typed parameter/result schemas of the standard capabilities.
// Third-party plugins import only this package; nothing from internal/
// crosses the plugin boundary.
//
// The host side (subprocess management, manifest verification,
// discovery and trust policy) lives in the secure-oci binary and is not
// part of the SDK surface.
package plugin

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// ContentType identifies protocol version v1 on the wire.
	ContentType = "application/vnd.platform-factory.rpc.v1+json"
	// LegacyContentType is the pre-rebrand Content-Type: still accepted
	// from a plugin process for the documented compatibility overlap
	// window (see docs/api-compatibility.md), never written by
	// WriteMessage.
	LegacyContentType = "application/vnd.secure-oci.rpc.v1+json"
	// ProtocolVersion is the version a plugin must report during the
	// handshake for a Client to consider it compatible.
	ProtocolVersion = "v1"

	maxMessageBytes    = 512 << 10
	maxHeaderBytes     = 16 << 10
	maxHeaderLineBytes = 4 << 10
)

// Request is one RPC call frame.
// TraceID is propagated from the host to the plugin for end-to-end correlation.
type Request struct {
	ID          string          `json:"id"`
	Method      string          `json:"method"`
	Params      json.RawMessage `json:"params,omitempty"`
	TraceID     string          `json:"trace_id,omitempty"`
	OperationID string          `json:"operation_id,omitempty"`
}

// Response is one RPC reply frame. Exactly one of Result or Error is set
// on a well-formed response.
// TraceID echoes back the request's TraceID for correlation.
type Response struct {
	ID          string          `json:"id"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       *RPCError       `json:"error,omitempty"`
	TraceID     string          `json:"trace_id,omitempty"`
	OperationID string          `json:"operation_id,omitempty"`
}

// RPCError is a protocol-level error returned by a plugin handler.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

// WriteMessage frames v (a Request or Response) as
//
//	Content-Type: application/vnd.secure-oci.rpc.v1+json
//	Content-Length: N
//
//	{...}
//
// and writes it to w.
func WriteMessage(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("plugin: encode message: %w", err)
	}
	if len(body) > maxMessageBytes {
		return fmt.Errorf("plugin: message of %d bytes exceeds the %d byte limit", len(body), maxMessageBytes)
	}
	header := fmt.Sprintf("Content-Type: %s\r\nContent-Length: %d\r\n\r\n", ContentType, len(body))
	if _, err := io.WriteString(w, header); err != nil {
		return fmt.Errorf("plugin: write header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("plugin: write body: %w", err)
	}
	return nil
}

// ReadMessage reads one framed message from r and returns its raw JSON
// body. It rejects a missing or oversized Content-Length and a
// Content-Type other than ContentType.
func ReadMessage(r *bufio.Reader) (json.RawMessage, error) {
	contentLength := -1
	sawContentType := false
	headerBytes := 0
	for {
		lineBytes, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) || len(lineBytes) > maxHeaderLineBytes {
			return nil, errors.New("plugin: header line exceeds limit")
		}
		headerBytes += len(lineBytes)
		if headerBytes > maxHeaderBytes {
			return nil, errors.New("plugin: headers exceed limit")
		}
		line := string(lineBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if line == "" {
					return nil, io.EOF
				}
				// A truncated header must not be mistaken for a clean
				// shutdown by a caller checking errors.Is(err, io.EOF), so
				// report it as an unexpected EOF instead of chaining the
				// original io.EOF.
				return nil, fmt.Errorf("plugin: read header: %w", io.ErrUnexpectedEOF)
			}
			return nil, fmt.Errorf("plugin: read header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("plugin: malformed header %q", line)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch strings.ToLower(key) {
		case "content-length":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 || n > maxMessageBytes {
				return nil, fmt.Errorf("plugin: invalid Content-Length %q", value)
			}
			contentLength = n
		case "content-type":
			if value != ContentType && value != LegacyContentType {
				return nil, fmt.Errorf("plugin: unsupported Content-Type %q, want %q", value, ContentType)
			}
			sawContentType = true
		}
	}
	if contentLength < 0 {
		return nil, errors.New("plugin: missing Content-Length header")
	}
	if !sawContentType {
		return nil, errors.New("plugin: missing Content-Type header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("plugin: read body: %w", err)
	}
	return json.RawMessage(body), nil
}

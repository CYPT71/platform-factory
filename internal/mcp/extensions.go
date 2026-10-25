package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/CYPT71/platform-factory/internal/extensions"
	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
	"io"
)

// Two routed tools keep fifty analyzers out of every model prompt.
func registerExtensions(s *Server) {
	s.AddTool(Tool{Name: "pf_extension_catalog", Description: "List fifty offline policy analyzers; use pf_extension_check with one name. No model calls, host files or network access.", Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
		type entry struct {
			Name        string `json:"name"`
			Category    string `json:"category"`
			Description string `json:"description"`
		}
		rows := []entry{}
		for _, p := range extensions.Catalog() {
			rows = append(rows, entry{p.Name, p.Category, p.Description})
		}
		b, err := json.Marshal(rows)
		return string(b), err
	}})
	s.AddTool(Tool{Name: "pf_extension_check", Description: "Run one offline analyzer on a supplied JSON object. Omit document to obtain its schema-by-example and checks. Findings never include document values.", InputSchema: json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"},"document":{"type":"object"}},"additionalProperties":false}`), Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
		if len(raw) > extensions.MaxInputBytes {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "input exceeds 1 MiB")
		}
		var args struct {
			Name     string          `json:"name"`
			Document json.RawMessage `json:"document"`
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&args); err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "expected name and optional document")
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "expected one object")
		}
		p, err := extensions.Lookup(args.Name)
		if err != nil {
			return "", toolerror.New(toolerror.ErrNotFound, "unknown extension")
		}
		if len(args.Document) == 0 {
			b, err := json.Marshal(p)
			return string(b), err
		}
		document, err := extensions.DecodeDocument(bytes.NewReader(args.Document))
		if err != nil {
			return "", toolerror.New(toolerror.ErrInvalidArgument, "document must be one bounded JSON object")
		}
		report, err := extensions.Analyze(args.Name, document)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(report)
		return string(b), err
	}})
}

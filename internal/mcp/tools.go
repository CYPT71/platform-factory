package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// ToolHandler implements one MCP tool. It receives the raw JSON
// "arguments" object from the tools/call request and returns the text
// to send back as the tool result. Returning a *toolError produces an
// isError:true tool result with a safe, structured message; returning
// any other error is treated as an internal failure and logged to
// stderr without exposing its text to the caller.
type ToolHandler func(ctx context.Context, arguments json.RawMessage) (string, error)

// Tool is one registered MCP tool: its wire descriptor plus the handler
// that implements it.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     ToolHandler
}

// toolRegistry holds every tool this server exposes, keyed by name.
type toolRegistry struct {
	tools map[string]Tool
}

func newToolRegistry() *toolRegistry {
	return &toolRegistry{tools: make(map[string]Tool)}
}

// Add registers a tool. It panics on a duplicate name - that is a
// programming error in this server's own wiring, not a runtime
// condition callers can trigger.
func (r *toolRegistry) Add(t Tool) {
	if t.Name == "" {
		panic("mcp: tool registered with an empty name")
	}
	if _, exists := r.tools[t.Name]; exists {
		panic(fmt.Sprintf("mcp: duplicate tool registration %q", t.Name))
	}
	if t.InputSchema == nil {
		t.InputSchema = json.RawMessage(`{"type":"object"}`)
	}
	r.tools[t.Name] = t
}

func (r *toolRegistry) get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// list returns every registered tool's descriptor, sorted by name so
// tools/list output is stable across calls.
func (r *toolRegistry) list() []toolDescriptor {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	descriptors := make([]toolDescriptor, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		descriptors = append(descriptors, toolDescriptor{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return descriptors
}

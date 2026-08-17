package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func newTestServer() *Server {
	s := NewServer("platform-factory", "test")
	s.AddTool(Tool{
		Name:        "echo",
		Description: "echoes the given text back",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Handler: func(ctx context.Context, arguments json.RawMessage) (string, error) {
			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(arguments, &args); err != nil {
				return "", newToolError(ErrInvalidArgument, "invalid arguments: %v", err)
			}
			if args.Text == "" {
				return "", newToolError(ErrInvalidArgument, "text must not be empty")
			}
			return args.Text, nil
		},
	})
	s.AddTool(Tool{
		Name:        "boom",
		Description: "always fails with an internal error",
		Handler: func(ctx context.Context, arguments json.RawMessage) (string, error) {
			return "", errBoom
		},
	})
	s.AddResource(Resource{
		URI:         "pf://project",
		Name:        "project",
		Description: "project summary",
		Handler: func(ctx context.Context) (string, string, error) {
			return `{"name":"platform-factory"}`, "application/json", nil
		},
	})
	return s
}

var errBoom = errFixed("boom")

type errFixed string

func (e errFixed) Error() string { return string(e) }

// runLines feeds each line as a separate stdin message and returns the
// decoded response lines, in order, that the server wrote to stdout.
func runLines(t *testing.T, s *Server, lines ...string) []map[string]any {
	t.Helper()
	stdin := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var stdout, stderr bytes.Buffer
	if err := s.Serve(context.Background(), stdin, &stdout, &stderr); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if stderr.Len() > 0 {
		t.Logf("stderr: %s", stderr.String())
	}

	var out []map[string]any
	dec := json.NewDecoder(&stdout)
	for {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			break
		}
		out = append(out, msg)
	}
	return out
}

func TestInitializeReturnsServerInfo(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d: %v", len(responses), responses)
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result field, got %v", responses[0])
	}
	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok || serverInfo["name"] != "platform-factory" {
		t.Fatalf("unexpected serverInfo: %v", result["serverInfo"])
	}
}

func TestNotificationsInitializedProducesNoResponse(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	)
	if len(responses) != 1 {
		t.Fatalf("expected exactly 1 response (notification must be silent), got %d: %v", len(responses), responses)
	}
}

func TestToolsListReturnsRegisteredToolsSortedByName(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result := responses[0]["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	first := tools[0].(map[string]any)
	if first["name"] != "boom" { // alphabetically before "echo"
		t.Fatalf("expected tools sorted by name, first was %v", first["name"])
	}
}

func TestToolsCallSuccessReturnsTextContent(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello"}}}`)
	result := responses[0]["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	if content["text"] != "hello" {
		t.Fatalf("unexpected content: %v", content)
	}
	if result["isError"] != nil {
		t.Fatalf("expected no isError field on success, got %v", result["isError"])
	}
}

func TestToolsCallToolErrorSetsIsErrorWithoutJSONRPCFailure(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":""}}}`)
	if responses[0]["error"] != nil {
		t.Fatalf("a tool-level failure must not be a JSON-RPC error: %v", responses[0]["error"])
	}
	result := responses[0]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("expected isError=true, got %v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)
	if !strings.Contains(content["text"].(string), ErrInvalidArgument) {
		t.Fatalf("expected the tool error code in the message, got %v", content["text"])
	}
}

func TestToolsCallInternalErrorDoesNotLeakRawErrorText(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	result := responses[0]["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	if strings.Contains(content["text"].(string), "boom") {
		t.Fatalf("internal error text leaked into the tool result: %v", content["text"])
	}
	if result["isError"] != true {
		t.Fatalf("expected isError=true, got %v", result)
	}
}

func TestToolsCallUnknownToolIsInvalidParams(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	errObj, ok := responses[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error for an unknown tool, got %v", responses[0])
	}
	if int(errObj["code"].(float64)) != errCodeInvalidParams {
		t.Fatalf("unexpected error code: %v", errObj["code"])
	}
}

func TestResourcesListAndRead(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"pf://project"}}`,
	)
	list := responses[0]["result"].(map[string]any)["resources"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(list))
	}
	read := responses[1]["result"].(map[string]any)
	contents := read["contents"].([]any)[0].(map[string]any)
	if contents["mimeType"] != "application/json" {
		t.Fatalf("unexpected mimeType: %v", contents["mimeType"])
	}
	if !strings.Contains(contents["text"].(string), "platform-factory") {
		t.Fatalf("unexpected text: %v", contents["text"])
	}
}

func TestResourcesReadUnknownURIIsInvalidParams(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"pf://nope"}}`)
	errObj, ok := responses[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", responses[0])
	}
	if int(errObj["code"].(float64)) != errCodeInvalidParams {
		t.Fatalf("unexpected error code: %v", errObj["code"])
	}
}

func TestUnknownMethodIsMethodNotFound(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{"jsonrpc":"2.0","id":1,"method":"totally/bogus"}`)
	errObj, ok := responses[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", responses[0])
	}
	if int(errObj["code"].(float64)) != errCodeMethodNotFound {
		t.Fatalf("unexpected error code: %v", errObj["code"])
	}
}

func TestUnknownNotificationIsSilentlyIgnored(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{"jsonrpc":"2.0","method":"totally/bogus"}`)
	if len(responses) != 0 {
		t.Fatalf("expected no response for an unknown notification, got %v", responses)
	}
}

func TestMalformedJSONProducesParseError(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{not json`)
	errObj, ok := responses[0]["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", responses[0])
	}
	if int(errObj["code"].(float64)) != errCodeParseError {
		t.Fatalf("unexpected error code: %v", errObj["code"])
	}
}

func TestPingReturnsEmptyResult(t *testing.T) {
	s := newTestServer()
	responses := runLines(t, s, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if _, ok := responses[0]["result"]; !ok {
		t.Fatalf("expected a result field, got %v", responses[0])
	}
}

func TestDuplicateToolRegistrationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic on duplicate tool registration")
		}
	}()
	s := NewServer("x", "0")
	s.AddTool(Tool{Name: "dup", Handler: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	s.AddTool(Tool{Name: "dup", Handler: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
}

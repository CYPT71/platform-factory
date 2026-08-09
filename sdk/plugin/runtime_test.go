package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

type referenceExtension struct{}

func (referenceExtension) Detect(context.Context, DetectParams) (DetectResult, error) {
	return DetectResult{Kind: "reference"}, nil
}

func TestReferenceRuntimeServesAllTypedCapabilities(t *testing.T) {
	runtime, err := NewRuntime("reference", "v1", referenceExtension{})
	if err != nil {
		t.Fatal(err)
	}
	call, cleanup := serveOverPipe(t, runtime.server)
	defer cleanup()

	for method, params := range map[string]any{
		"v1.detect": DetectParams{Path: "/project"},
		"v1.freeze": FreezeParams{Language: "reference", Root: "/project"},
		"v1.plan":   PlanParams{Language: "reference", Root: "/project"},
	} {
		if response := call(method, params); response.Error != nil {
			t.Fatalf("%s: %+v", method, response.Error)
		}
	}
	bad := call("v1.detect", map[string]any{"path": "/project", "unknown": true})
	if bad.Error == nil || bad.Error.Code != 500 {
		t.Fatalf("unknown typed field response = %+v", bad)
	}

	// Exercise Runtime.Serve itself, including clean EOF.
	var input, output bytes.Buffer
	if err := WriteMessage(&input, Request{ID: "1", Method: "v1.detect", Params: json.RawMessage(`{"path":"/project"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Serve(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
}
func (referenceExtension) Freeze(context.Context, FreezeParams) (FreezeResult, error) {
	return FreezeResult{Steps: [][]string{{"tool", "freeze"}}}, nil
}
func (referenceExtension) Plan(context.Context, PlanParams) (PlanResult, error) {
	return PlanResult{Notes: []string{"reference"}}, nil
}

func TestReferenceRuntimeRegistersLanguageContract(t *testing.T) {
	runtime, err := NewRuntime("reference", "v1", referenceExtension{})
	if err != nil {
		t.Fatal(err)
	}
	if runtime == nil || len(runtime.server.capabilities) != 3 {
		t.Fatalf("runtime=%+v", runtime)
	}
}

func TestReferenceRuntimeRejectsIncompleteConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, version string
		extension     LanguageExtension
	}{{"", "v1", referenceExtension{}}, {"reference", "", referenceExtension{}}, {"reference", "v1", nil}} {
		if runtime, err := NewRuntime(test.name, test.version, test.extension); err == nil || runtime != nil {
			t.Fatalf("NewRuntime(%q, %q, %T) = %+v, %v", test.name, test.version, test.extension, runtime, err)
		}
	}
}

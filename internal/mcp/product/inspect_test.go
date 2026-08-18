package product

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestStatusToolHandlerRejectsMalformedJSON(t *testing.T) {
	_, err := StatusToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestStatusToolHandlerRejectsPathTraversalInDirectory(t *testing.T) {
	_, err := StatusToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"directory":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal directory")
	}
}

func TestStatusToolHandlerDefaultsFormatToJSON(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := StatusToolHandler(repoRoot)(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	want := []string{"--format", "json"}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

func TestStatusToolHandlerAppendsDirectoryAndFormat(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := StatusToolHandler(repoRoot)(context.Background(), json.RawMessage(`{"directory":"proj","format":"text"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Command != "status" {
		t.Fatalf("command = %q, want %q", result.Command, "status")
	}
	want := []string{"--format", "text", "proj"}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

func TestDoctorToolHandlerRejectsMalformedJSON(t *testing.T) {
	_, err := DoctorToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestDoctorToolHandlerDefaultsToAllScopeWithoutAppendingIt(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	for _, payload := range []string{`{}`, `{"scope":"all"}`} {
		out, err := DoctorToolHandler(repoRoot)(context.Background(), json.RawMessage(payload))
		if err != nil {
			t.Fatalf("unexpected error for payload %s: %v", payload, err)
		}
		var result Result
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		want := []string{"--json"}
		if !reflect.DeepEqual(result.Args, want) {
			t.Fatalf("payload %s: args = %v\nwant  %v", payload, result.Args, want)
		}
	}
}

func TestDoctorToolHandlerAppendsARestrictedScope(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := DoctorToolHandler(repoRoot)(context.Background(), json.RawMessage(`{"scope":"publish"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	want := []string{"--json", "publish"}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

func TestDetectToolHandlerRejectsMalformedJSON(t *testing.T) {
	_, err := DetectToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestDetectToolHandlerRejectsPathTraversal(t *testing.T) {
	_, err := DetectToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"path":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal path")
	}
}

func TestDetectToolHandlerBuildsArgsAndDefaultsFormat(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := DetectToolHandler(repoRoot)(context.Background(), json.RawMessage(`{"path":"proj"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	want := []string{"--format", "json", "proj"}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

func TestDetectToolHandlerBuildsArgsWithAcceptAmbiguousAndFormat(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := DetectToolHandler(repoRoot)(context.Background(), json.RawMessage(`{"path":"proj","format":"text","accept_ambiguous":true}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Command != "detect" {
		t.Fatalf("command = %q, want %q", result.Command, "detect")
	}
	want := []string{"--format", "text", "--accept-ambiguous", "proj"}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

func TestVerifyToolHandlerRejectsMalformedJSON(t *testing.T) {
	_, err := VerifyToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestVerifyToolHandlerRejectsPathTraversalInLayout(t *testing.T) {
	_, err := VerifyToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{"layout":"../escape"}`))
	if err == nil {
		t.Fatal("expected an error for a path-traversal layout")
	}
}

func TestVerifyToolHandlerBuildsArgsAndDefaultsFormat(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := VerifyToolHandler(repoRoot)(context.Background(), json.RawMessage(`{"layout":"oci"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Command != "verify" {
		t.Fatalf("command = %q, want %q", result.Command, "verify")
	}
	want := []string{"--format", "json", "oci"}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

// TestInspectToolHandlerBuildsArgs covers InspectToolHandler, which
// shared layoutToolHandler's other test paths above never exercised
// (VerifyToolHandler and InspectToolHandler are two distinct wrappers
// around the same private helper, each wiring a different subcommand).
func TestInspectToolHandlerBuildsArgs(t *testing.T) {
	withHelperProcess(t, 0)
	repoRoot := t.TempDir()
	out, err := InspectToolHandler(repoRoot)(context.Background(), json.RawMessage(`{"layout":"oci","format":"text"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Command != "inspect" {
		t.Fatalf("command = %q, want %q", result.Command, "inspect")
	}
	want := []string{"--format", "text", "oci"}
	if !reflect.DeepEqual(result.Args, want) {
		t.Fatalf("args = %v\nwant  %v", result.Args, want)
	}
}

func TestInspectToolHandlerRejectsMalformedJSON(t *testing.T) {
	_, err := InspectToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestInspectToolHandlerRequiresALayout(t *testing.T) {
	_, err := InspectToolHandler(t.TempDir())(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected an error when layout is missing")
	}
}

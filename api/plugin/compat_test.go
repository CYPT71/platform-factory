package plugin

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// TestCompatFixturesDecodeExactly decodes real, on-disk wire frames
// (testdata/compat) into the current typed shapes and asserts every field,
// not just "no decode error" - encoding/json silently drops fields it no
// longer recognizes, so a field rename would otherwise pass unnoticed on an
// emptied-out struct. A fixture must never be edited to keep this test
// passing; a fixture failing here is a real wire-protocol compatibility
// break for third-party plugins built against v1 and must be fixed in
// code, not in testdata.
func TestCompatFixturesDecodeExactly(t *testing.T) {
	t.Run("hello_result.json", func(t *testing.T) {
		var got HelloResult
		decodeCompatFixture(t, "hello_result.json", &got)
		want := HelloResult{
			APIVersion:   "v1",
			Name:         "example-plugin",
			Version:      "1.4.2",
			Capabilities: []string{"detect", "freeze", "plan"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%+v want=%+v", got, want)
		}
	})

	t.Run("detect_params.json", func(t *testing.T) {
		var got DetectParams
		decodeCompatFixture(t, "detect_params.json", &got)
		want := DetectParams{Path: "/workspace/project"}
		if got != want {
			t.Fatalf("got=%+v want=%+v", got, want)
		}
	})

	t.Run("detect_result.json", func(t *testing.T) {
		var got DetectResult
		decodeCompatFixture(t, "detect_result.json", &got)
		want := DetectResult{Kind: "static", Profile: "rust", Evidence: []string{"Cargo.toml", "Cargo.lock"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%+v want=%+v", got, want)
		}
	})

	t.Run("freeze_params.json", func(t *testing.T) {
		var got FreezeParams
		decodeCompatFixture(t, "freeze_params.json", &got)
		want := FreezeParams{Language: "rust", Root: "/workspace/project"}
		if got != want {
			t.Fatalf("got=%+v want=%+v", got, want)
		}
	})

	t.Run("freeze_result.json", func(t *testing.T) {
		var got FreezeResult
		decodeCompatFixture(t, "freeze_result.json", &got)
		want := FreezeResult{
			Steps:   [][]string{{"cargo", "generate-lockfile"}, {"cargo", "vendor", "vendor"}},
			Profile: "rust",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%+v want=%+v", got, want)
		}
	})

	t.Run("plan_params.json", func(t *testing.T) {
		var got PlanParams
		decodeCompatFixture(t, "plan_params.json", &got)
		want := PlanParams{Language: "rust", Root: "/workspace/project"}
		if got != want {
			t.Fatalf("got=%+v want=%+v", got, want)
		}
	})

	t.Run("plan_result.json", func(t *testing.T) {
		var got PlanResult
		decodeCompatFixture(t, "plan_result.json", &got)
		want := PlanResult{Notes: []string{"vendor directory is committed", "release profile strips symbols"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got=%+v want=%+v", got, want)
		}
	})

	t.Run("request.json", func(t *testing.T) {
		var got Request
		decodeCompatFixture(t, "request.json", &got)
		if got.ID != "1" || got.Method != "v1.detect" {
			t.Fatalf("got=%+v", got)
		}
		var params DetectParams
		if err := json.Unmarshal(got.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params != (DetectParams{Path: "/workspace/project"}) {
			t.Fatalf("params=%+v", params)
		}
	})

	t.Run("response_ok.json", func(t *testing.T) {
		var got Response
		decodeCompatFixture(t, "response_ok.json", &got)
		if got.ID != "1" || got.Error != nil {
			t.Fatalf("got=%+v", got)
		}
		var result DetectResult
		if err := json.Unmarshal(got.Result, &result); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result, DetectResult{Kind: "static", Profile: "rust"}) {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("response_error.json", func(t *testing.T) {
		var got Response
		decodeCompatFixture(t, "response_error.json", &got)
		if got.ID != "1" || got.Result != nil || got.Error == nil {
			t.Fatalf("got=%+v", got)
		}
		want := RPCError{Code: 404, Message: "unknown method v1.scan"}
		if *got.Error != want {
			t.Fatalf("error=%+v want=%+v", *got.Error, want)
		}
	})
}

func decodeCompatFixture(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile("testdata/compat/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

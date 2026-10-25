package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"github.com/CYPT71/platform-factory/internal/extensions"
	sdk "github.com/CYPT71/platform-factory/sdk/plugin"
	"testing"
)

func TestAllProfilesNegotiateAndEvaluateRealRPC(t *testing.T) {
	for _, p := range extensions.Catalog() {
		t.Run(p.Name, func(t *testing.T) {
			for _, test := range []struct {
				doc  map[string]any
				pass bool
			}{{p.Example, true}, {p.RejectedExample, false}} {
				params, _ := json.Marshal(map[string]any{"document": test.doc})
				var input, output bytes.Buffer
				if err := sdk.WriteMessage(&input, sdk.Request{ID: "hello", Method: "v1.hello"}); err != nil {
					t.Fatal(err)
				}
				if err := sdk.WriteMessage(&input, sdk.Request{ID: "check", Method: "v1.analyzer.scan", Params: params}); err != nil {
					t.Fatal(err)
				}
				server, err := newServer(p.Name)
				if err != nil {
					t.Fatal(err)
				}
				if err := server.Serve(context.Background(), &input, &output); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(&output)
				hello, err := sdk.ReadMessage(reader)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(hello, []byte(p.Name)) {
					t.Fatalf("wrong identity: %s", hello)
				}
				raw, err := sdk.ReadMessage(reader)
				if err != nil {
					t.Fatal(err)
				}
				var response sdk.Response
				if err := json.Unmarshal(raw, &response); err != nil {
					t.Fatal(err)
				}
				var report extensions.Report
				if err := json.Unmarshal(response.Result, &report); err != nil {
					t.Fatal(err)
				}
				if response.Error != nil || report.Passed != test.pass {
					t.Fatalf("unexpected report: %s", raw)
				}
			}
		})
	}
}

package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// FuzzDecodeRequest feeds arbitrary bytes at decodeRequest, the strict
// JSON body decoder every control-plane HTTP handler uses -
// register/heartbeat/lease submit/complete/cancel. This is the wire
// protocol between platform-factory-worker and platform-factory-control-plane: an
// attacker on the network, or simply a buggy/mismatched worker version,
// controls these bytes completely. Fuzzed against all four request
// shapes in one target since decodeRequest's behavior (unknown-field and
// trailing-data rejection) doesn't depend on which struct it decodes
// into.
func FuzzDecodeRequest(f *testing.F) {
	f.Add(`{"platform":"linux/amd64"}`)
	f.Add(`{"payload":"work","required_platform":"linux/amd64","tenant":"acme"}`)
	f.Add(`{"lease_id":"lease-1","result":"done"}`)
	f.Add(`{}`)
	f.Add(`null`)
	f.Add(``)
	f.Add(`{"unknown_field":true}`)
	f.Add(`{"platform":"linux/amd64"}{"platform":"linux/arm64"}`)
	f.Add(`{"platform":` + strings.Repeat(`[`, 500) + `}`)

	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 1<<16 {
			t.Skip()
		}
		for _, target := range []any{
			&registerRequest{}, &submitRequest{}, &completeRequest{}, &cancelRequest{},
		} {
			req := httptest.NewRequest("POST", "/fuzz", strings.NewReader(body))
			rec := httptest.NewRecorder()
			_ = decodeRequest(rec, req, target)
		}
	})
}

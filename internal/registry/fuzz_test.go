package registry

import (
	"bytes"
	"context"
	"net/http"
	"testing"
)

// FuzzGetManifestResponse and FuzzGetBlobResponse feed arbitrary bytes as
// a registry's HTTP response body. A registry is a network peer, possibly
// compromised or simply broken, and GetManifest/GetBlob must never panic
// on whatever it sends back - only verify, or fail closed.

func FuzzGetManifestResponse(f *testing.F) {
	f.Add([]byte(`{"schemaVersion":2,"manifests":[]}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))

	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		target := Reference{Registry: "registry.example", Repository: "team/service"}
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
			return response(http.StatusOK, "", bytes.NewReader(body))
		})}}
		_, _, _ = client.GetManifest(context.Background(), target, "v1")
	})
}

func FuzzGetBlobResponse(f *testing.F) {
	f.Add([]byte("arbitrary blob bytes"))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<16 {
			t.Skip()
		}
		target := Reference{Registry: "registry.example", Repository: "team/service"}
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
			return response(http.StatusOK, "", bytes.NewReader(body))
		})}}
		_, _ = client.GetBlob(context.Background(), target, "sha256:"+repeatHex())
	})
}

func repeatHex() string {
	digits := "0123456789abcdef"
	out := make([]byte, 64)
	for i := range out {
		out[i] = digits[i%len(digits)]
	}
	return string(out)
}

package registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestArtifactFallbackPreservesIndexAndDeduplicates(t *testing.T) {
	subject := "sha256:" + strings.Repeat("a", 64)
	old := `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:` + strings.Repeat("b", 64) + `","size":10,"artifactType":"old","annotations":{"keep":"yes"}}`
	index := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","annotations":{"publisher":"preserved"},"manifests":[` + old + `]}`)
	fallbackWrites := 0
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		if r.Method == http.MethodHead {
			return response(http.StatusOK, "", nil)
		}
		if strings.Contains(r.URL.Path, "/manifests/sha256-") {
			if r.Method == http.MethodGet {
				return response(http.StatusOK, "", strings.NewReader(string(index)))
			}
			if r.Method == http.MethodPut {
				index, _ = io.ReadAll(r.Body)
				fallbackWrites++
				return response(http.StatusCreated, "", nil)
			}
		}
		if r.Method == http.MethodPut {
			return response(http.StatusCreated, "", nil)
		}
		return response(http.StatusInternalServerError, "", nil)
	})}}
	target := Reference{Registry: "registry.example", Repository: "app", Tag: "v1"}
	for _, payload := range []string{"first", "second", "second"} {
		if _, err := client.PushArtifact(context.Background(), target, subject, artifactMediaType, 100, "test/type", "application/json", []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	var result struct {
		Manifests   []json.RawMessage `json:"manifests"`
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(index, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Manifests) != 3 || fallbackWrites != 2 || result.Annotations["publisher"] != "preserved" {
		t.Fatalf("index=%s writes=%d", index, fallbackWrites)
	}
	if !strings.Contains(string(result.Manifests[0]), `"keep":"yes"`) {
		t.Fatal("existing descriptor metadata lost")
	}
}

func TestListReferrersRejectsMalformedSubjectDigest(t *testing.T) {
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
		t.Fatal("ListReferrers must reject a malformed subject digest before making any request")
		return nil
	})}}
	if _, err := client.ListReferrers(context.Background(), Reference{Registry: "registry.example", Repository: "app"}, "not-a-digest"); err == nil {
		t.Fatal("expected an error for a malformed subject digest")
	}
}

func TestListReferrersSurfacesUnexpectedStatusCodes(t *testing.T) {
	subject := "sha256:" + strings.Repeat("a", 64)
	target := Reference{Registry: "registry.example", Repository: "app"}

	t.Run("native-error-status", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
			return response(http.StatusInternalServerError, "", strings.NewReader("boom"))
		})}}
		if _, err := client.ListReferrers(context.Background(), target, subject); err == nil {
			t.Fatal("expected an error for a non-200/404 native referrers response")
		}
	})

	t.Run("fallback-not-found", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
			return response(http.StatusNotFound, "", strings.NewReader("not found"))
		})}}
		got, err := client.ListReferrers(context.Background(), target, subject)
		if err != nil || got != nil {
			t.Fatalf("got=%+v err=%v, want nil,nil when neither the native endpoint nor the fallback tag exist", got, err)
		}
	})

	t.Run("fallback-error-status", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
			if strings.Contains(r.URL.Path, "/manifests/sha256-") {
				return response(http.StatusForbidden, "", strings.NewReader("denied"))
			}
			return response(http.StatusNotFound, "", strings.NewReader("not found"))
		})}}
		if _, err := client.ListReferrers(context.Background(), target, subject); err == nil {
			t.Fatal("expected an error for a non-200/404 fallback response")
		}
	})

	t.Run("oversized-native-body", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
			return response(http.StatusOK, "", strings.NewReader(strings.Repeat(" ", maxResponseBody+1)))
		})}}
		if _, err := client.ListReferrers(context.Background(), target, subject); err == nil {
			t.Fatal("expected an error for a referrers index past the size limit")
		}
	})

	t.Run("malformed-json", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
			return response(http.StatusOK, "", strings.NewReader("not json"))
		})}}
		if _, err := client.ListReferrers(context.Background(), target, subject); err == nil {
			t.Fatal("expected an error for a non-JSON referrers index")
		}
	})

	t.Run("wrong-schema-version", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
			return response(http.StatusOK, "", strings.NewReader(`{"schemaVersion":1,"manifests":[]}`))
		})}}
		if _, err := client.ListReferrers(context.Background(), target, subject); err == nil {
			t.Fatal("expected an error for an unexpected schemaVersion")
		}
	})

	t.Run("invalid-descriptor-digest", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
			return response(http.StatusOK, "", strings.NewReader(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"not-a-digest","size":1}]}`))
		})}}
		if _, err := client.ListReferrers(context.Background(), target, subject); err == nil {
			t.Fatal("expected an error for a referrer descriptor with an invalid digest")
		}
	})

	t.Run("negative-descriptor-size", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
			return response(http.StatusOK, "", strings.NewReader(`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:`+strings.Repeat("b", 64)+`","size":-1}]}`))
		})}}
		if _, err := client.ListReferrers(context.Background(), target, subject); err == nil {
			t.Fatal("expected an error for a referrer descriptor with a negative size")
		}
	})
}

func TestArtifactSubjectAcknowledgementAndFallbackFailures(t *testing.T) {
	subject := "sha256:" + strings.Repeat("a", 64)
	for _, kind := range []string{"acknowledged", "wrong-subject", "absent-index", "non-index", "malformed-index", "denied", "oversized-index"} {
		t.Run(kind, func(t *testing.T) {
			reads, writes := 0, 0
			var installed []byte
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
				if r.Method == http.MethodHead {
					return response(http.StatusOK, "", nil)
				}
				if strings.Contains(r.URL.Path, "/manifests/sha256-") {
					if r.Method == http.MethodPut {
						writes++
						installed, _ = io.ReadAll(r.Body)
						return response(http.StatusCreated, "", nil)
					}
					reads++
					if len(installed) > 0 {
						return response(http.StatusOK, "", strings.NewReader(string(installed)))
					}
					switch kind {
					case "absent-index":
						return response(http.StatusNotFound, "", nil)
					case "non-index":
						return response(http.StatusOK, "", strings.NewReader(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","manifests":[]}`))
					case "malformed-index":
						return response(http.StatusOK, "", strings.NewReader(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":{}}`))
					case "denied":
						return response(http.StatusForbidden, "", nil)
					case "oversized-index":
						return response(http.StatusOK, "", strings.NewReader(strings.Repeat(" ", maxResponseBody+1)))
					}
					return response(http.StatusInternalServerError, "", nil)
				}
				result := response(http.StatusCreated, "", nil)
				if kind == "acknowledged" {
					result.Header.Set("OCI-Subject", subject)
				}
				if kind == "wrong-subject" {
					result.Header.Set("OCI-Subject", "sha256:"+strings.Repeat("b", 64))
				}
				return result
			})}}
			_, err := client.PushArtifact(context.Background(), Reference{Registry: "registry.example", Repository: "app"}, subject, artifactMediaType, 100, "test/type", "application/json", []byte("payload"))
			if kind == "acknowledged" {
				if err != nil || reads != 0 || writes != 0 {
					t.Fatalf("err=%v reads=%d writes=%d", err, reads, writes)
				}
			} else if kind == "absent-index" {
				if err != nil || writes != 1 {
					t.Fatalf("err=%v writes=%d", err, writes)
				}
			} else if err == nil || writes != 0 {
				t.Fatalf("unsafe fallback accepted: err=%v writes=%d", err, writes)
			}
		})
	}
}

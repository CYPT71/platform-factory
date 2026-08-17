package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type dataThenErrorReader struct {
	data []byte
	err  error
}

func (r *dataThenErrorReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestManifestAndBlobTransportFailuresRemainFailures(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	wantDigest := "sha256:" + strings.Repeat("a", 64)

	for _, tc := range []struct {
		name string
		call func(*Client) error
	}{
		{name: "manifest transport", call: func(c *Client) error { _, _, err := c.GetManifest(context.Background(), target, "v1"); return err }},
		{name: "blob transport", call: func(c *Client) error { _, err := c.GetBlob(context.Background(), target, wantDigest); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{HTTP: &http.Client{Transport: roundTripErrorFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("transport sentinel")
			})}}
			if err := tc.call(client); err == nil || !strings.Contains(err.Error(), "transport sentinel") {
				t.Fatalf("transport failure lost: %v", err)
			}
		})
	}

	readFailure := errors.New("read sentinel")
	for _, tc := range []struct {
		name string
		call func(*Client) error
	}{
		{name: "manifest body", call: func(c *Client) error { _, _, err := c.GetManifest(context.Background(), target, "v1"); return err }},
		{name: "blob body", call: func(c *Client) error { _, err := c.GetBlob(context.Background(), target, wantDigest); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
				return response(http.StatusOK, "", io.NopCloser(failingReader{err: readFailure}))
			})}}
			if err := tc.call(client); err == nil || !strings.Contains(err.Error(), "read sentinel") {
				t.Fatalf("body failure lost: %v", err)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	client := &Client{HTTP: &http.Client{Transport: roundTripErrorFunc(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})}}
	if _, err := client.GetBlob(cancelled, target, wantDigest); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause not preserved: %v", err)
	}
}

func TestRegistryClientRefusesAutomaticRedirects(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		result := response(http.StatusTemporaryRedirect, "", strings.NewReader("redirect"))
		result.Header.Set("Location", "https://other.example/steal")
		return result
	})}}
	if _, _, err := client.GetManifest(context.Background(), target, "latest"); err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("manifest redirect accepted: %v", err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := client.GetBlob(context.Background(), target, digest); err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("blob redirect accepted: %v", err)
	}
}

func TestUploadOffsetRejectsMalformedOrUnprovenState(t *testing.T) {
	uploadURL, _ := url.Parse("https://registry.example/upload/1")
	cases := []struct {
		name       string
		status     int
		rangeValue string
		wantOffset int64
		wantError  string
	}{
		{name: "empty", status: http.StatusNoContent, wantOffset: 0},
		{name: "accepted", status: http.StatusAccepted, rangeValue: "bytes=0-8", wantOffset: 9},
		{name: "missing dash", status: http.StatusNoContent, rangeValue: "bytes=9", wantError: "invalid upload Range"},
		{name: "negative", status: http.StatusNoContent, rangeValue: "bytes=0--1", wantError: "invalid upload Range"},
		{name: "non numeric", status: http.StatusNoContent, rangeValue: "bytes=0-last", wantError: "invalid upload Range"},
		{name: "server failure", status: http.StatusInternalServerError, wantError: "query upload offset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
				result := response(tc.status, "", strings.NewReader("remote error"))
				result.Header.Set("Range", tc.rangeValue)
				return result
			})}}
			got, err := client.uploadOffset(context.Background(), uploadURL)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("offset=%d err=%v", got, err)
				}
				return
			}
			if err != nil || got != tc.wantOffset {
				t.Fatalf("offset=%d want=%d err=%v", got, tc.wantOffset, err)
			}
		})
	}
}

func TestResumeUploadDiscardsInvalidAndStaleSessions(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	digest := "sha256:" + strings.Repeat("b", 64)
	for _, tc := range []struct {
		name     string
		contents string
	}{
		{name: "malformed", contents: "{"},
		{name: "unknown field", contents: fmt.Sprintf(`{"digest":%q,"size":7,"url":"https://registry.example/upload/1","token":"must-not-be-accepted"}`, digest)},
		{name: "wrong digest", contents: `{"digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","size":7,"url":"https://registry.example/upload/1"}`},
		{name: "wrong size", contents: fmt.Sprintf(`{"digest":%q,"size":8,"url":"https://registry.example/upload/1"}`, digest)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			client := &Client{SessionDir: dir}
			path := client.uploadSessionPath(target, digest)
			if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			_, offset, resumed, err := client.resumeUpload(context.Background(), target, digest, 7)
			if err != nil || resumed || offset != 0 {
				t.Fatalf("offset=%d resumed=%v err=%v", offset, resumed, err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid session retained: %v", err)
			}
		})
	}

	dir := t.TempDir()
	client := &Client{SessionDir: dir, HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
		return response(http.StatusGone, "", nil)
	})}}
	location, _ := url.Parse("https://registry.example/upload/stale")
	if err := client.saveUpload(target, digest, 7, location); err != nil {
		t.Fatal(err)
	}
	_, _, resumed, err := client.resumeUpload(context.Background(), target, digest, 7)
	if err != nil || resumed {
		t.Fatalf("stale session resumed=%v err=%v", resumed, err)
	}
}

func TestUploadBlobReconcilesPartialPatchAndRejectsImpossibleOffsets(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	payload := []byte("abcdef")
	digest := "sha256:" + strings.Repeat("d", 64)

	t.Run("partial committed range", func(t *testing.T) {
		patches := 0
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
			switch r.Method {
			case http.MethodPost:
				result := response(http.StatusAccepted, "", nil)
				result.Header.Set("Location", "/upload/1")
				return result
			case http.MethodPatch:
				patches++
				if patches == 1 {
					return response(http.StatusInternalServerError, "", nil)
				}
				return response(http.StatusAccepted, "", nil)
			case http.MethodGet:
				result := response(http.StatusNoContent, "", nil)
				result.Header.Set("Range", "bytes=0-2")
				return result
			case http.MethodPut:
				return response(http.StatusCreated, "", nil)
			default:
				t.Fatalf("unexpected method %s", r.Method)
				return nil
			}
		})}}
		if err := client.uploadBlob(context.Background(), target, digest, int64(len(payload)), bytes.NewReader(payload)); err != nil {
			t.Fatal(err)
		}
		if patches != 2 {
			t.Fatalf("patches=%d want 2", patches)
		}
	})

	t.Run("offset beyond chunk", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
			if r.Method == http.MethodPost {
				result := response(http.StatusAccepted, "", nil)
				result.Header.Set("Location", "/upload/1")
				return result
			}
			if r.Method == http.MethodPatch {
				return response(http.StatusInternalServerError, "", nil)
			}
			result := response(http.StatusNoContent, "", nil)
			result.Header.Set("Range", "bytes=0-99")
			return result
		})}}
		err := client.uploadBlob(context.Background(), target, digest, int64(len(payload)), bytes.NewReader(payload))
		if err == nil || !strings.Contains(err.Error(), "outside current chunk") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestAuthorizeRejectsMalformedChallengesAndTokenResponses(t *testing.T) {
	for _, challenge := range []string{"Basic realm=x", "Bearer", `Bearer realm=":://bad"`, `Bearer realm="http://auth.example/token"`} {
		t.Run(challenge, func(t *testing.T) {
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
				return response(http.StatusOK, "", strings.NewReader(`{"token":"x"}`))
			})}}
			if err := client.authorize(context.Background(), challenge); err == nil {
				t.Fatalf("challenge accepted: %q", challenge)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "service failure", status: http.StatusForbidden, body: "denied"},
		{name: "empty token", status: http.StatusOK, body: `{}`},
		{name: "access token", status: http.StatusOK, body: `{"access_token":"fallback"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
				return response(tc.status, "", strings.NewReader(tc.body))
			})}}
			err := client.authorize(context.Background(), `Bearer realm="https://auth.example/token",service="registry"`)
			if tc.name == "access token" {
				if err != nil || client.currentToken() != "fallback" {
					t.Fatalf("token=%q err=%v", client.currentToken(), err)
				}
			} else if err == nil {
				t.Fatal("invalid token response accepted")
			}
		})
	}
}

func TestSessionPersistenceFailuresAreReported(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	digest := "sha256:" + strings.Repeat("e", 64)
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &Client{SessionDir: blocker}
	location, _ := url.Parse("https://registry.example/upload/1")
	if err := client.saveUpload(target, digest, 1, location); err == nil || !strings.Contains(err.Error(), "create upload session directory") {
		t.Fatalf("persistence failure=%v", err)
	}
}

func TestAuthenticatedRequestIsReplayedAndRetryFailuresSurface(t *testing.T) {
	endpoint, _ := url.Parse("https://registry.example/v2/team/service/manifests/v1")
	for _, failRetry := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail_retry_%v", failRetry), func(t *testing.T) {
			registryCalls := 0
			client := &Client{Username: "user", Password: "password"}
			client.HTTP = &http.Client{Transport: roundTripErrorFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "auth.example" {
					if user, password, ok := r.BasicAuth(); !ok || user != "user" || password != "password" {
						t.Fatalf("token credentials not propagated")
					}
					return response(http.StatusOK, "", strings.NewReader(`{"token":"issued"}`)), nil
				}
				registryCalls++
				if registryCalls == 1 {
					result := response(http.StatusUnauthorized, "", nil)
					result.Header.Set("WWW-Authenticate", `Bearer realm="https://auth.example/token"`)
					return result, nil
				}
				if failRetry {
					return nil, errors.New("authenticated retry sentinel")
				}
				if r.Header.Get("Authorization") != "Bearer issued" {
					t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != "payload" {
					t.Fatalf("replayed body=%q err=%v", body, err)
				}
				return response(http.StatusCreated, "", nil), nil
			})}
			got, err := client.doURL(context.Background(), http.MethodPut, endpoint, bytes.NewReader([]byte("payload")), "application/json", "application/json", 7)
			if failRetry {
				if err == nil || !strings.Contains(err.Error(), "authenticated retry sentinel") {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil || got.StatusCode != http.StatusCreated {
				t.Fatalf("response=%v err=%v", got, err)
			}
			_ = got.Body.Close()
		})
	}
}

func TestUploadBlobRejectsInvalidInitiationResumeAndCompletion(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	digest := "sha256:" + strings.Repeat("f", 64)
	payload := []byte("payload")
	for _, tc := range []struct {
		name     string
		status   int
		location string
		want     string
	}{
		{name: "server rejected", status: http.StatusForbidden, want: "start blob upload"},
		{name: "missing location", status: http.StatusAccepted, want: "no Location"},
		{name: "cross host", status: http.StatusAccepted, location: "https://evil.example/upload", want: "changed registry host"},
		{name: "mounted", status: http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
				result := response(tc.status, "", strings.NewReader("denied"))
				result.Header.Set("Location", tc.location)
				return result
			})}}
			err := client.uploadBlob(context.Background(), target, digest, int64(len(payload)), bytes.NewReader(payload))
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}

	for _, source := range []string{"/owner/repo", "owner/repo/", "owner/../repo", "owner bad/repo"} {
		client := &Client{MountFrom: source}
		if err := client.uploadBlob(context.Background(), target, digest, 0, bytes.NewReader(nil)); err == nil || !strings.Contains(err.Error(), "invalid cross-repository") {
			t.Fatalf("mount source %q accepted: %v", source, err)
		}
	}

	t.Run("resume beyond size", func(t *testing.T) {
		dir := t.TempDir()
		client := &Client{SessionDir: dir, HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) *http.Response {
			result := response(http.StatusNoContent, "", nil)
			result.Header.Set("Range", "bytes=0-99")
			return result
		})}}
		location, _ := url.Parse("https://registry.example/upload/1")
		if err := client.saveUpload(target, digest, int64(len(payload)), location); err != nil {
			t.Fatal(err)
		}
		err := client.uploadBlob(context.Background(), target, digest, int64(len(payload)), bytes.NewReader(payload))
		if err == nil || !strings.Contains(err.Error(), "exceeds blob size") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("finish rejected", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
			switch r.Method {
			case http.MethodPost:
				result := response(http.StatusAccepted, "", nil)
				result.Header.Set("Location", "/upload/1")
				return result
			case http.MethodPatch:
				return response(http.StatusAccepted, "", nil)
			default:
				return response(http.StatusBadRequest, "", strings.NewReader("finish denied"))
			}
		})}}
		err := client.uploadBlob(context.Background(), target, digest, int64(len(payload)), bytes.NewReader(payload))
		if err == nil || !strings.Contains(err.Error(), "finish blob upload") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestPutManifestDefaultsMediaTypeAndRejectsRemoteFailure(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
		if r.Header.Get("Content-Type") != indexMediaType {
			t.Fatalf("content type=%q", r.Header.Get("Content-Type"))
		}
		return response(http.StatusConflict, "", strings.NewReader("conflict"))
	})}}
	if err := client.putManifest(context.Background(), target, "v1", "", []byte("{}")); err == nil || !strings.Contains(err.Error(), "put manifest") {
		t.Fatalf("err=%v", err)
	}
}

func TestUploadBlobDoesNotHideSourceOrReconciliationErrors(t *testing.T) {
	target := Reference{Registry: "registry.example", Repository: "team/service"}
	digest := "sha256:" + strings.Repeat("1", 64)

	t.Run("source reader", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) *http.Response {
			if r.Method == http.MethodPost {
				result := response(http.StatusAccepted, "", nil)
				result.Header.Set("Location", "/upload/1")
				return result
			}
			return response(http.StatusAccepted, "", nil)
		})}}
		err := client.uploadBlob(context.Background(), target, digest, 3, &dataThenErrorReader{data: []byte("abc"), err: errors.New("source sentinel")})
		if err == nil || !strings.Contains(err.Error(), "source sentinel") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("patch and reconciliation transport", func(t *testing.T) {
		client := &Client{HTTP: &http.Client{Transport: roundTripErrorFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodPost {
				result := response(http.StatusAccepted, "", nil)
				result.Header.Set("Location", "/upload/1")
				return result, nil
			}
			return nil, errors.New("upload transport sentinel")
		})}}
		err := client.uploadBlob(context.Background(), target, digest, 1, bytes.NewReader([]byte("x")))
		if err == nil || !strings.Contains(err.Error(), "upload transport sentinel") {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("remove session", func(t *testing.T) {
		root := t.TempDir()
		blocker := filepath.Join(root, "file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		client := &Client{SessionDir: blocker}
		if err := client.removeUpload(target, digest); err == nil || !strings.Contains(err.Error(), "remove upload session") {
			t.Fatalf("err=%v", err)
		}
	})
}

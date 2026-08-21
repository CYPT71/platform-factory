package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// roundTripFunc mirrors internal/registry/client_test.go's own fake
// transport convention, so this package's HTTP tests follow the same
// pattern already established for external HTTP clients in this repo.
type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request), nil
}

func TestFromEnvIsUnavailableWithoutAnAPIKey(t *testing.T) {
	t.Setenv(apiKeyEnv, "")
	if _, ok := FromEnv(); ok {
		t.Fatal("expected FromEnv to report unavailable without an API key")
	}
}

func TestFromEnvUsesTheConfiguredModelOrDefault(t *testing.T) {
	t.Setenv(apiKeyEnv, "test-key")
	t.Setenv(modelEnv, "")
	client, ok := FromEnv()
	if !ok {
		t.Fatal("expected FromEnv to report available")
	}
	if client.Model != defaultModel {
		t.Fatalf("model=%q", client.Model)
	}

	t.Setenv(modelEnv, "claude-custom")
	client, ok = FromEnv()
	if !ok || client.Model != "claude-custom" {
		t.Fatalf("client=%+v ok=%v", client, ok)
	}
}

func TestCompleteReturnsTheTextContent(t *testing.T) {
	client := &Client{
		APIKey: "test-key", Model: "claude-test", BaseURL: apiURL,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) *http.Response {
			if req.Header.Get("x-api-key") != "test-key" {
				t.Fatalf("missing x-api-key header")
			}
			if req.Header.Get("anthropic-version") != apiVersion {
				t.Fatalf("missing anthropic-version header")
			}
			var decoded messagesRequest
			body, _ := io.ReadAll(req.Body)
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.System != "sys" || len(decoded.Messages) != 1 || decoded.Messages[0].Content != "hello" {
				t.Fatalf("decoded=%+v", decoded)
			}
			payload, _ := json.Marshal(messagesResponse{
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{{Type: "text", Text: "world"}},
			})
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload))}
		})},
	}
	text, err := client.Complete(context.Background(), "sys", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if text != "world" {
		t.Fatalf("text=%q", text)
	}
}

func TestCompleteSurfacesAnAPIError(t *testing.T) {
	client := &Client{
		APIKey: "test-key", Model: "claude-test", BaseURL: apiURL,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) *http.Response {
			payload := `{"error":{"type":"invalid_request_error","message":"bad request"}}`
			return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(payload))}
		})},
	}
	_, err := client.Complete(context.Background(), "sys", "hello")
	if err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompleteFailsClosedOnEmptyTextContent(t *testing.T) {
	client := &Client{
		APIKey: "test-key", Model: "claude-test", BaseURL: apiURL,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) *http.Response {
			payload := `{"content":[],"stop_reason":"end_turn"}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}
		})},
	}
	_, err := client.Complete(context.Background(), "sys", "hello")
	if err == nil {
		t.Fatal("expected an error for a response with no text content")
	}
}

// TestCompleteAgainstRealAnthropicAPI is a real, opt-in, env-var-gated
// integration test - skipped by default, matching how
// cmd/platform-factory/imagepull_test.go gates its own real Docker Hub
// pull test in the sibling platform-factory repo. Run with:
//
//	PLATFORM_FACTORY_TEST_LIVE_ANTHROPIC=1 PLATFORM_FACTORY_MCP_ANTHROPIC_API_KEY=sk-... go test ./internal/mcp/agent/... -run RealAnthropic
func TestCompleteAgainstRealAnthropicAPI(t *testing.T) {
	if os.Getenv("PLATFORM_FACTORY_TEST_LIVE_ANTHROPIC") != "1" {
		t.Skip("set PLATFORM_FACTORY_TEST_LIVE_ANTHROPIC=1 (and a real API key) to run this live test")
	}
	client, ok := FromEnv()
	if !ok {
		t.Fatal("PLATFORM_FACTORY_MCP_ANTHROPIC_API_KEY must be set for this live test")
	}
	text, err := client.Complete(context.Background(), "Reply with exactly one word.", "Say hello.")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(text) == "" {
		t.Fatal("expected a non-empty reply from the real API")
	}
}

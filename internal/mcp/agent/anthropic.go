// Package agent implements the MCP server's opt-in, server-embedded LLM
// orchestration: pf_plugin_modify, the free-text mode of pf_core_patch,
// and pf_implement. It is only active when
// PLATFORM_FACTORY_MCP_ANTHROPIC_API_KEY is set; every tool built on it
// degrades to a clear, structured error naming the client-orchestrated
// primitives to use instead when the key is unset - it never silently
// no-ops or fabricates a result. Like this server's other subpackages,
// it has no dependency on internal/mcp itself.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	apiKeyEnv    = "PLATFORM_FACTORY_MCP_ANTHROPIC_API_KEY"
	modelEnv     = "PLATFORM_FACTORY_MCP_ANTHROPIC_MODEL"
	defaultModel = "claude-sonnet-4-5"
	apiURL       = "https://api.anthropic.com/v1/messages"
	apiVersion   = "2023-06-01"

	// maxResponseBytes bounds how much of the Anthropic response body
	// this client will read, matching internal/registry/client.go's own
	// io.LimitReader discipline for any external HTTP response.
	maxResponseBytes = 4 << 20
)

// Client is a minimal Anthropic Messages API client: plain net/http, an
// injectable *http.Client (for tests), and one method. It holds no more
// surface than pf_plugin_modify/pf_core_patch/pf_implement actually need.
type Client struct {
	APIKey     string
	Model      string
	HTTPClient *http.Client
	BaseURL    string
}

// FromEnv returns a Client configured from PLATFORM_FACTORY_MCP_ANTHROPIC_API_KEY
// and, optionally, PLATFORM_FACTORY_MCP_ANTHROPIC_MODEL. ok is false when
// no API key is configured - callers must treat that as "the
// server-embedded agent is not available here", never as an error to
// retry.
func FromEnv() (client *Client, ok bool) {
	key := strings.TrimSpace(os.Getenv(apiKeyEnv))
	if key == "" {
		return nil, false
	}
	model := strings.TrimSpace(os.Getenv(modelEnv))
	if model == "" {
		model = defaultModel
	}
	return &Client{
		APIKey:     key,
		Model:      model,
		HTTPClient: &http.Client{Timeout: 120 * time.Second},
		BaseURL:    apiURL,
	}, true
}

type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends one system+user turn (no conversation state - every
// call in this package is a single bounded exchange, never a
// free-running chat) and returns the model's text reply.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(messagesRequest{
		Model:     c.Model,
		MaxTokens: 8192,
		System:    system,
		Messages:  []message{{Role: "user", Content: user}},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", apiVersion)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic: request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("anthropic: read response: %w", err)
	}

	var parsed messagesResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", fmt.Errorf("anthropic: decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil {
			return "", fmt.Errorf("anthropic: %s: %s", parsed.Error.Type, parsed.Error.Message)
		}
		return "", fmt.Errorf("anthropic: unexpected status %d", resp.StatusCode)
	}
	var text strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if text.Len() == 0 {
		return "", fmt.Errorf("anthropic: response contained no text content (stop_reason %q)", parsed.StopReason)
	}
	return text.String(), nil
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return apiURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// Package deepseek is the Neural Hive integration with the DeepSeek chat-completions API.
//
// It implements the provider-agnostic llm.Provider interface, so a Neural Hive agent that holds
// an llm.Wrapper can be pointed at DeepSeek without changing its code. The HTTP transport,
// bearer authentication, request construction, response parsing, model configuration, timeouts
// and error handling all live here, in exactly one place.
//
// The API key is read from configuration and is only ever placed in the Authorization header of
// an outgoing request. It is never logged, never returned in an error message and never
// included in a result.
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/neural-hive/hive-node/internal/llm"
)

const (
	// DefaultBaseURL is the stock DeepSeek API endpoint.
	DefaultBaseURL = "https://api.deepseek.com"
	// DefaultModel is the model Neural Hive ships with.
	DefaultModel = "deepseek-chat"
	// DefaultTimeout bounds a single completion request.
	DefaultTimeout = 120 * time.Second
	// Provider is the name reported for this backend.
	Provider = "deepseek"
	// chatPath is the DeepSeek chat completion route.
	chatPath = "/chat/completions"
	// modelsPath lists the models the endpoint serves.
	modelsPath = "/models"
)

// ErrNoAPIKey is returned when the client was constructed without an API key. It deliberately
// never contains the key.
var ErrNoAPIKey = errors.New("deepseek: no API key configured (set DEEPSEEK_API_KEY)")

// Client is the raw HTTP transport to one DeepSeek endpoint.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type wireRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type wireError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type wireResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int         `json:"index"`
		Message      wireMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *wireError `json:"error"`
}

// NewClient builds a transport client. An empty baseURL falls back to DefaultBaseURL and a
// non-positive timeout falls back to DefaultTimeout. The key may be empty: the client then
// reports ErrNoAPIKey on every call instead of silently succeeding.
func NewClient(apiKey, baseURL string, timeout time.Duration, hc *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: strings.TrimSpace(apiKey), http: hc}
}

// BaseURL returns the normalised endpoint.
func (c *Client) BaseURL() string { return c.baseURL }

// HasKey reports whether an API key was configured, without revealing it.
func (c *Client) HasKey() bool { return c.apiKey != "" }

// Name implements llm.Provider.
func (c *Client) Name() string { return Provider }

// Complete implements llm.Provider: one non-streaming POST /chat/completions.
func (c *Client) Complete(ctx context.Context, req llm.Request) (*llm.Result, error) {
	if !c.HasKey() {
		return nil, ErrNoAPIKey
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = DefaultModel
	}
	msgs := make([]wireMessage, 0, len(req.Messages)+1)
	if s := strings.TrimSpace(req.System); s != "" && (len(req.Messages) == 0 || req.Messages[0].Role != llm.RoleSystem) {
		msgs = append(msgs, wireMessage{Role: llm.RoleSystem, Content: s})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, wireMessage{Role: m.Role, Content: m.Content})
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("deepseek: no messages to send")
	}
	body, err := json.Marshal(wireRequest{Model: model, Messages: msgs, Stream: false, Temperature: req.Temperature, MaxTokens: req.MaxTokens})
	if err != nil {
		return nil, fmt.Errorf("deepseek: encode request: %w", err)
	}
	start := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+chatPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("deepseek: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("deepseek: request to %s failed: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError(resp.StatusCode, raw)
	}
	var out wireResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("deepseek: decode response: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, fmt.Errorf("deepseek: model error: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("deepseek: response contained no choices")
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return nil, fmt.Errorf("deepseek: model %s returned an empty completion", model)
	}
	respModel := out.Model
	if respModel == "" {
		respModel = model
	}
	return &llm.Result{
		Text:             text,
		Model:            respModel,
		Provider:         Provider,
		Duration:         time.Since(start),
		PromptTokens:     out.Usage.PromptTokens,
		CompletionTokens: out.Usage.CompletionTokens,
		FinishReason:     out.Choices[0].FinishReason,
		RequestID:        out.ID,
	}, nil
}

// statusError maps a non-200 status onto an actionable error without ever echoing the key.
func (c *Client) statusError(code int, raw []byte) error {
	var e struct {
		Error wireError `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	msg := strings.TrimSpace(e.Error.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	switch code {
	case http.StatusUnauthorized:
		return fmt.Errorf("deepseek: unauthorized (401): the API key is missing or invalid")
	case http.StatusPaymentRequired:
		return fmt.Errorf("deepseek: insufficient balance (402): %s", msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("deepseek: rate limited (429): %s", msg)
	case http.StatusBadRequest:
		return fmt.Errorf("deepseek: bad request (400): %s", msg)
	case http.StatusServiceUnavailable, http.StatusBadGateway:
		return fmt.Errorf("deepseek: provider unavailable (%d): %s", code, msg)
	default:
		return fmt.Errorf("deepseek: provider returned %d: %s", code, msg)
	}
}

// Models lists the model identifiers the endpoint advertises.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	if !c.HasKey() {
		return nil, ErrNoAPIKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+modelsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("deepseek: build models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepseek: reach %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError(resp.StatusCode, raw)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("deepseek: decode models: %w", err)
	}
	names := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			names = append(names, m.ID)
		}
	}
	return names, nil
}

// Available checks that the endpoint answers and the configured model is advertised. It is used
// by the agent health check; an unreachable endpoint or an invalid key is reported, never hidden.
func (c *Client) Available(ctx context.Context, model string) error {
	models, err := c.Models(ctx)
	if err != nil {
		return err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = DefaultModel
	}
	for _, m := range models {
		if m == model {
			return nil
		}
	}
	if len(models) == 0 {
		return fmt.Errorf("deepseek: endpoint %s advertises no models", c.baseURL)
	}
	return fmt.Errorf("deepseek: model %s is not advertised by %s (available: %s)", model, c.baseURL, strings.Join(models, ", "))
}

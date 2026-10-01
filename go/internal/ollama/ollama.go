// Package ollama is the reusable Neural Hive integration with Ollama-compatible LLM endpoints.
//
// A Neural Hive agent never talks to a language model directly: it goes through this package so
// the transport (base URL, model, timeout, system prompt, response parsing and error handling)
// lives in exactly one place. Adding a second Ollama-backed agent later (research, planner,
// validator) means reusing this package, not writing another HTTP integration.
//
// Nothing here fabricates a completion. Every transport error, non-HTTP-200 status, model-side
// error and empty completion is returned to the caller so it can propagate to the user interface.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the stock local Ollama endpoint.
	DefaultBaseURL = "http://127.0.0.1:11434"
	// DefaultModel is the model Neural Hive ships with.
	DefaultModel = "llama3.2:latest"
	// DefaultTimeout bounds a single generation request.
	DefaultTimeout = 120 * time.Second
	// chatPath is the Ollama chat completion route.
	chatPath = "/api/chat"
	// tagsPath lists the models the endpoint serves.
	tagsPath = "/api/tags"
)

// Provider is the name reported for this backend.
const Provider = "ollama"

// Message is a single chat turn in the Ollama wire format.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is one non-streaming chat completion request.
type ChatRequest struct {
	Model    string
	Messages []Message
	Options  map[string]interface{}
}

type wireRequest struct {
	Model    string                 `json:"model"`
	Messages []Message              `json:"messages"`
	Stream   bool                   `json:"stream"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

type wireResponse struct {
	Model         string  `json:"model"`
	Message       Message `json:"message"`
	Done          bool    `json:"done"`
	DoneReason    string  `json:"done_reason"`
	TotalDuration int64   `json:"total_duration"`
	EvalCount     int64   `json:"eval_count"`
	Error         string  `json:"error"`
}

// Result is a successful completion plus the evidence the UI can display.
type Result struct {
	Text       string
	Model      string
	Provider   string
	DurationNS int64
	EvalCount  int64
}

// Client is the raw HTTP transport to one Ollama-compatible endpoint.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient builds a transport client. An empty baseURL falls back to the default and a
// non-positive timeout falls back to the default timeout.
func NewClient(baseURL string, timeout time.Duration, hc *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: hc}
}

// BaseURL returns the normalised endpoint.
func (c *Client) BaseURL() string { return c.baseURL }

// Chat performs one non-streaming chat completion against POST /api/chat.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*Result, error) {
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("ollama: no model configured")
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("ollama: no messages supplied")
	}
	body, err := json.Marshal(wireRequest{Model: req.Model, Messages: req.Messages, Stream: false, Options: req.Options})
	if err != nil {
		return nil, fmt.Errorf("ollama: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+chatPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama: request to %s failed: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("ollama: %s returned %d: %s", c.baseURL, resp.StatusCode, e.Error)
		}
		return nil, fmt.Errorf("ollama: %s returned %d: %s", c.baseURL, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out wireResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ollama: decode response: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("ollama: model error: %s", out.Error)
	}
	text := strings.TrimSpace(out.Message.Content)
	if text == "" {
		return nil, fmt.Errorf("ollama: model %s returned an empty completion", req.Model)
	}
	model := out.Model
	if model == "" {
		model = req.Model
	}
	return &Result{Text: text, Model: model, Provider: Provider, DurationNS: out.TotalDuration, EvalCount: out.EvalCount}, nil
}

// Models returns the model tags the endpoint currently serves.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+tagsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("ollama: build models request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: reach %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: %s returned %d: %s", c.baseURL, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("ollama: decode models: %w", err)
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// Config parameterises a Wrapper. All fields are optional: unset fields fall back to defaults.
type Config struct {
	BaseURL     string
	Model       string
	System      string
	Temperature float64
	Timeout     time.Duration
	HTTPClient  *http.Client
}

// Wrapper is the agent-facing facade over the transport. An agent holds a Wrapper and calls
// Ask or Chat; it never touches the Ollama HTTP API itself.
type Wrapper struct {
	client      *Client
	model       string
	system      string
	temperature float64
}

// NewWrapper builds a wrapper, applying defaults for any unset field.
func NewWrapper(cfg Config) *Wrapper {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	return &Wrapper{
		client:      NewClient(cfg.BaseURL, cfg.Timeout, cfg.HTTPClient),
		model:       model,
		system:      strings.TrimSpace(cfg.System),
		temperature: cfg.Temperature,
	}
}

// Model returns the configured model tag.
func (w *Wrapper) Model() string { return w.model }

// Provider returns the backend name reported to the UI.
func (w *Wrapper) Provider() string { return Provider }

// BaseURL returns the endpoint the wrapper talks to.
func (w *Wrapper) BaseURL() string { return w.client.BaseURL() }

// System returns the configured system prompt.
func (w *Wrapper) System() string { return w.system }

// Available checks that the endpoint answers and that the configured model is installed.
func (w *Wrapper) Available(ctx context.Context) error {
	models, err := w.client.Models(ctx)
	if err != nil {
		return err
	}
	for _, m := range models {
		if m == w.model {
			return nil
		}
	}
	if len(models) == 0 {
		return fmt.Errorf("ollama: endpoint %s serves no models", w.client.BaseURL())
	}
	return fmt.Errorf("ollama: model %s is not installed at %s (available: %s)", w.model, w.client.BaseURL(), strings.Join(models, ", "))
}

// Ask sends a single user prompt, prefixing the configured system prompt when present.
func (w *Wrapper) Ask(ctx context.Context, prompt string) (*Result, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("ollama: empty prompt")
	}
	msgs := make([]Message, 0, 2)
	if w.system != "" {
		msgs = append(msgs, Message{Role: "system", Content: w.system})
	}
	msgs = append(msgs, Message{Role: "user", Content: prompt})
	return w.Chat(ctx, msgs)
}

// Chat sends a full conversation to the configured model.
func (w *Wrapper) Chat(ctx context.Context, messages []Message) (*Result, error) {
	req := ChatRequest{Model: w.model, Messages: messages}
	if w.temperature > 0 {
		req.Options = map[string]interface{}{"temperature": w.temperature}
	}
	return w.client.Chat(ctx, req)
}

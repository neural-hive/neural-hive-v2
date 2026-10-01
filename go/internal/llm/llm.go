// Package llm is the provider-agnostic language-model layer of Neural Hive.
//
// An agent never talks to a model vendor directly. It holds a *Wrapper, which holds a
// Provider. Swapping DeepSeek for another vendor is a configuration change plus one new
// Provider implementation: the agent code, the request shape and the error handling stay
// identical. Adding a second model-backed agent (research, planner, validator) reuses the
// same wrapper and the same provider instead of writing another HTTP integration.
//
// The package never fabricates a completion. Every transport error, HTTP status, vendor-side
// error and empty completion is returned to the caller, so it can reach the user interface.
package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Chat roles understood by every provider.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one chat turn in the vendor-neutral format.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request describes one completion. A provider maps it onto its own wire format.
type Request struct {
	// Model overrides the wrapper default when non-empty.
	Model string
	// System is an optional system prompt the provider prepends when the conversation does not
	// already start with a system turn.
	System string
	// Messages is the conversation to complete (at least one entry).
	Messages []Message
	// Temperature is the sampling temperature; providers may ignore a zero value.
	Temperature float64
	// MaxTokens bounds the completion; zero means the provider default.
	MaxTokens int
}

// Result is a successful completion plus the evidence the user interface can display. It never
// carries an API key or any other secret.
type Result struct {
	Text             string
	Model            string
	Provider         string
	Duration         time.Duration
	PromptTokens     int
	CompletionTokens int
	FinishReason     string
	RequestID        string
}

// Provider is the boundary between an agent and a model vendor.
type Provider interface {
	// Name is the provider identifier reported to the UI, for example "deepseek".
	Name() string
	// Complete runs one non-streaming completion.
	Complete(ctx context.Context, req Request) (*Result, error)
}

// Wrapper is the agent-facing facade. An agent holds a *Wrapper and calls Ask or Chat; it never
// touches a vendor HTTP API itself. It applies the configured defaults (model, system prompt,
// temperature) to every request and stamps the provider and model onto the result.
type Wrapper struct {
	provider    Provider
	model       string
	system      string
	temperature float64
}

// Config parameterises a Wrapper.
type Config struct {
	// Provider is required. A nil provider makes every call fail with a clear error.
	Provider Provider
	// Model is the default model name; empty keeps the provider default.
	Model string
	// System is the default system prompt.
	System string
	// Temperature is the default sampling temperature.
	Temperature float64
}

// NewWrapper builds a wrapper from configuration.
func NewWrapper(cfg Config) *Wrapper {
	return &Wrapper{
		provider:    cfg.Provider,
		model:       strings.TrimSpace(cfg.Model),
		system:      strings.TrimSpace(cfg.System),
		temperature: cfg.Temperature,
	}
}

// Model returns the configured default model.
func (w *Wrapper) Model() string { return w.model }

// Provider returns the provider name, or "unconfigured" when no provider was supplied.
func (w *Wrapper) Provider() string {
	if w.provider == nil {
		return "unconfigured"
	}
	return w.provider.Name()
}

// System returns the configured system prompt.
func (w *Wrapper) System() string { return w.system }

// Ask sends a single user prompt, prefixing the configured system prompt when present.
func (w *Wrapper) Ask(ctx context.Context, prompt string) (*Result, error) {
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("llm: empty prompt")
	}
	msgs := make([]Message, 0, 2)
	if w.system != "" {
		msgs = append(msgs, Message{Role: RoleSystem, Content: w.system})
	}
	msgs = append(msgs, Message{Role: RoleUser, Content: prompt})
	return w.Chat(ctx, msgs)
}

// Chat sends a full conversation with the wrapper defaults applied.
func (w *Wrapper) Chat(ctx context.Context, messages []Message) (*Result, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("llm: no messages supplied")
	}
	if w.provider == nil {
		return nil, fmt.Errorf("llm: no provider configured")
	}
	res, err := w.provider.Complete(ctx, Request{
		Model:       w.model,
		System:      w.system,
		Messages:    messages,
		Temperature: w.temperature,
	})
	if err != nil {
		return nil, err
	}
	if res.Provider == "" {
		res.Provider = w.provider.Name()
	}
	if res.Model == "" {
		res.Model = w.model
	}
	return res, nil
}

package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neural-hive/hive-node/internal/llm"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := NewClient("test-key", srv.URL, 5*time.Second, srv.Client())
	return c, srv
}

const okResponse = `{"id":"req_1","model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"hello from deepseek"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":9,"total_tokens":14}}`

func TestCompleteBuildsAuthenticatedRequest(t *testing.T) {
	var gotAuth, gotCT, gotPath, gotMethod string
	var body wireRequest
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(okResponse))
	})
	defer srv.Close()

	res, err := c.Complete(context.Background(), llm.Request{
		Model:    "deepseek-chat",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "What is distributed consensus?"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s", gotMethod)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Fatalf("content-type = %q", gotCT)
	}
	if body.Model != "deepseek-chat" || body.Stream {
		t.Fatalf("wire request model=%q stream=%v", body.Model, body.Stream)
	}
	if len(body.Messages) != 1 || body.Messages[0].Content != "What is distributed consensus?" {
		t.Fatalf("messages = %+v", body.Messages)
	}
	if res.Text != "hello from deepseek" {
		t.Fatalf("text = %q", res.Text)
	}
	if res.Model != "deepseek-chat" || res.Provider != Provider {
		t.Fatalf("model/provider = %q/%q", res.Model, res.Provider)
	}
	if res.PromptTokens != 5 || res.CompletionTokens != 9 {
		t.Fatalf("usage = %d/%d", res.PromptTokens, res.CompletionTokens)
	}
	if res.RequestID != "req_1" || res.FinishReason != "stop" {
		t.Fatalf("id/finish = %q/%q", res.RequestID, res.FinishReason)
	}
}

func TestCompletePrependsSystemAndKeepsConfiguredModel(t *testing.T) {
	var body wireRequest
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(okResponse))
	})
	defer srv.Close()
	if _, err := c.Complete(context.Background(), llm.Request{
		Model:    "deepseek-reasoner",
		System:   "be terse",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "q"}},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if body.Model != "deepseek-reasoner" {
		t.Fatalf("model = %q", body.Model)
	}
	if len(body.Messages) != 2 || body.Messages[0].Role != llm.RoleSystem || body.Messages[0].Content != "be terse" {
		t.Fatalf("system not prepended: %+v", body.Messages)
	}
}

func TestCompleteDefaultsModelWhenUnset(t *testing.T) {
	var body wireRequest
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(okResponse))
	})
	defer srv.Close()
	if _, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "q"}}}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if body.Model != DefaultModel {
		t.Fatalf("default model = %q", body.Model)
	}
}

func TestMissingAPIKey(t *testing.T) {
	c := NewClient("", "http://127.0.0.1:1", time.Second, nil)
	if c.HasKey() {
		t.Fatalf("HasKey must be false without a key")
	}
	if _, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}}); err != ErrNoAPIKey {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
	if _, err := c.Models(context.Background()); err != ErrNoAPIKey {
		t.Fatalf("models err = %v, want ErrNoAPIKey", err)
	}
}

func TestUnauthorizedMapsToClearError(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Authentication Fails","type":"authentication_error","code":"invalid_api_key"}}`))
	})
	defer srv.Close()
	_, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "unauthorized") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestRateLimitMapsToClearError(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit reached","type":"rate_limit_error"}}`))
	})
	defer srv.Close()
	_, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v", err)
	}
}

func TestServerErrorReported(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal"}}`))
	})
	defer srv.Close()
	_, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
}

func TestMalformedResponse(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json at all`))
	})
	defer srv.Close()
	if _, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}}); err == nil {
		t.Fatalf("expected a decode error")
	}
}

func TestEmptyCompletionRejected(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"   "},"finish_reason":"stop"}]}`))
	})
	defer srv.Close()
	if _, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}}); err == nil {
		t.Fatalf("expected an empty-completion error")
	}
}

func TestNoChoicesRejected(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"deepseek-chat","choices":[]}`))
	})
	defer srv.Close()
	if _, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}}); err == nil {
		t.Fatalf("expected a no-choices error")
	}
}

func TestProviderErrorEnvelope(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"message":"model overloaded","type":"server_error"}}`))
	})
	defer srv.Close()
	_, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("err = %v", err)
	}
}

func TestTimeoutPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(250 * time.Millisecond)
		_, _ = w.Write([]byte(okResponse))
	}))
	defer srv.Close()
	c := NewClient("k", srv.URL, 30*time.Millisecond, nil)
	if _, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}}); err == nil {
		t.Fatalf("expected a timeout error")
	}
}

func TestModelsAndAvailable(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"}]}`))
	})
	defer srv.Close()
	names, err := c.Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(names) != 2 || names[0] != "deepseek-chat" {
		t.Fatalf("names = %v", names)
	}
	if err := c.Available(context.Background(), "deepseek-chat"); err != nil {
		t.Fatalf("available: %v", err)
	}
	if err := c.Available(context.Background(), "does-not-exist"); err == nil {
		t.Fatalf("expected an unavailable-model error")
	}
}

func TestAPIKeyNeverLeaksInErrors(t *testing.T) {
	const key = "sk-super-secret-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal"}}`))
	}))
	defer srv.Close()
	c := NewClient(key, srv.URL, time.Second, nil)
	_, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err == nil {
		t.Fatalf("expected an error")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("error leaked the API key: %v", err)
	}
}

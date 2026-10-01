package deepworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neural-hive/hive-node/internal/llm"
)

type fakeProvider struct {
	key   string
	err   error
	reply string
}

func (f fakeProvider) Name() string { return "fake" }

func (f fakeProvider) HasKey() bool { return f.key != "" }

func (f fakeProvider) Complete(ctx context.Context, req llm.Request) (*llm.Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &llm.Result{Text: f.reply, Model: "fake-model", Provider: "fake", RequestID: "req-1"}, nil
}

func TestHealthAndInfo(t *testing.T) {
	srv := New(Config{ID: "agent-01", Index: 1, Port: 9401, Tags: []string{"research", "analysis"}, Model: "m"}, fakeProvider{key: "sk-x"})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != 200 {
		t.Fatalf("health code %d", rec.Code)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["ok"] != true {
		t.Fatalf("ok not true: %v", body)
	}
	if body["agentId"] != "agent-01" {
		t.Fatalf("agentId: %v", body)
	}
}

func TestHealthUnavailableWithoutKey(t *testing.T) {
	srv := New(Config{ID: "a", Tags: []string{"x"}}, fakeProvider{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	if rec.Code != 503 {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}

func TestTaskReturnsAnswerWithProvenance(t *testing.T) {
	srv := New(Config{ID: "agent-02", Index: 2, Port: 9402, Tags: []string{"coding"}, Model: "m"}, fakeProvider{key: "sk-x", reply: "hello world"})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/task", strings.NewReader(`{"subTaskId":"st-1","prompt":"do it"}`)))
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	var out TaskResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Answer != "hello world" {
		t.Fatalf("answer %q", out.Answer)
	}
	if out.SubTaskID != "st-1" {
		t.Fatalf("subtask %q", out.SubTaskID)
	}
	if out.Provider != "fake" {
		t.Fatalf("provider %q", out.Provider)
	}
	if out.Port != 9402 {
		t.Fatalf("port %d", out.Port)
	}
}

func TestTaskErrorIsSurfaced(t *testing.T) {
	srv := New(Config{ID: "agent-03", Tags: []string{"x"}}, fakeProvider{key: "sk-x", err: fmt.Errorf("boom")})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/task", strings.NewReader(`{"subTaskId":"st-2","prompt":"do it"}`)))
	if rec.Code != 502 {
		t.Fatalf("want 502, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("error not surfaced: %s", rec.Body.String())
	}
}

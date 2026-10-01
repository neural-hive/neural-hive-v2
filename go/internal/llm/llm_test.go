package llm

import (
	"context"
	"errors"
	"testing"
)

type fakeProvider struct {
	last Request
	out  *Result
	err  error
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Complete(ctx context.Context, req Request) (*Result, error) {
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	if f.out != nil {
		return f.out, nil
	}
	return &Result{Text: "ok", Model: req.Model, Provider: "fake"}, nil
}

func TestWrapperAppliesDefaults(t *testing.T) {
	p := &fakeProvider{}
	w := NewWrapper(Config{Provider: p, Model: "m1", System: "sys", Temperature: 0.3})
	if w.Model() != "m1" || w.Provider() != "fake" || w.System() != "sys" {
		t.Fatalf("wrapper defaults not applied: model=%q provider=%q system=%q", w.Model(), w.Provider(), w.System())
	}
	res, err := w.Ask(context.Background(), "hello")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Text != "ok" {
		t.Fatalf("text = %q", res.Text)
	}
	if p.last.Model != "m1" || p.last.Temperature != 0.3 || p.last.System != "sys" {
		t.Fatalf("defaults not forwarded: %+v", p.last)
	}
	if len(p.last.Messages) != 2 || p.last.Messages[1].Role != RoleUser || p.last.Messages[1].Content != "hello" {
		t.Fatalf("user message not forwarded: %+v", p.last.Messages)
	}
}

func TestAskPrefixesSystemPrompt(t *testing.T) {
	p := &fakeProvider{}
	w := NewWrapper(Config{Provider: p, System: "be nice"})
	if _, err := w.Ask(context.Background(), "q"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if len(p.last.Messages) != 2 || p.last.Messages[0].Role != RoleSystem || p.last.Messages[0].Content != "be nice" {
		t.Fatalf("system prompt not prefixed: %+v", p.last.Messages)
	}
}

func TestEmptyPromptRejected(t *testing.T) {
	w := NewWrapper(Config{Provider: &fakeProvider{}})
	if _, err := w.Ask(context.Background(), "   "); err == nil {
		t.Fatalf("expected an empty-prompt error")
	}
}

func TestNilProviderFails(t *testing.T) {
	w := NewWrapper(Config{})
	if w.Provider() != "unconfigured" {
		t.Fatalf("provider = %q", w.Provider())
	}
	if _, err := w.Ask(context.Background(), "x"); err == nil {
		t.Fatalf("expected an error without a provider")
	}
}

func TestProviderErrorPropagates(t *testing.T) {
	sentinel := errors.New("boom")
	w := NewWrapper(Config{Provider: &fakeProvider{err: sentinel}})
	if _, err := w.Ask(context.Background(), "x"); !errors.Is(err, sentinel) {
		t.Fatalf("provider error not propagated, got %v", err)
	}
}

func TestChatStampsProviderAndModel(t *testing.T) {
	p := &fakeProvider{out: &Result{Text: "hi"}}
	w := NewWrapper(Config{Provider: p, Model: "m2"})
	res, err := w.Chat(context.Background(), []Message{{Role: RoleUser, Content: "x"}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Provider != "fake" || res.Model != "m2" {
		t.Fatalf("stamp failed: provider=%q model=%q", res.Provider, res.Model)
	}
}

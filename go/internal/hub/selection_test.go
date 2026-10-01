package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neural-hive/hive-node/internal/config"
)

// TestSelectPrefersTagMatch checks the deterministic routing prefers the agent whose tags fit.
func TestSelectPrefersTagMatch(t *testing.T) {
	reg := NewRegistry(" ")
	reg.Register(AgentCard{ID: "agent-a", Port: 9401, Tags: []string{"research"}, Status: "online"})
	reg.Register(AgentCard{ID: "agent-b", Port: 9402, Tags: []string{"coding"}, Status: "online"})
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}}, reg, fakeProvider{}, time.Second)
	card, sel := h.Select(Subtask{ID: "st-1", Tags: []string{"coding"}}, map[string]bool{})
	if card.ID != "agent-b" {
		t.Fatalf("selected %s, want agent-b", card.ID)
	}
	if len(sel.MatchingTags) != 1 || sel.MatchingTags[0] != "coding" {
		t.Fatalf("matching tags %v", sel.MatchingTags)
	}
}

func TestSelectSkipsUnhealthyAgents(t *testing.T) {
	reg := NewRegistry(" ")
	reg.Register(AgentCard{ID: "agent-a", Port: 9401, Tags: []string{"research"}, Status: "offline"})
	reg.Register(AgentCard{ID: "agent-b", Port: 9402, Tags: []string{"research"}, Status: "online"})
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}}, reg, fakeProvider{}, time.Second)
	card, _ := h.Select(Subtask{ID: "st-1", Tags: []string{"research"}}, map[string]bool{})
	if card.ID != "agent-b" {
		t.Fatalf("selected %s, want agent-b", card.ID)
	}
}

func TestSelectAvoidsUsedAgents(t *testing.T) {
	reg := NewRegistry(" ")
	reg.Register(AgentCard{ID: "agent-a", Port: 9401, Tags: []string{"research"}, Status: "online"})
	reg.Register(AgentCard{ID: "agent-b", Port: 9402, Tags: []string{"research"}, Status: "online"})
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}}, reg, fakeProvider{}, time.Second)
	card, _ := h.Select(Subtask{ID: "st-1", Tags: []string{"research"}}, map[string]bool{"agent-a": true})
	if card.ID != "agent-b" {
		t.Fatalf("selected %s, want agent-b (agent-a already used)", card.ID)
	}
}

func TestRegistryHealthAndOnlineCount(t *testing.T) {
	reg := NewRegistry(" ")
	reg.Register(AgentCard{ID: "agent-b", Status: "unknown"})
	reg.Register(AgentCard{ID: "agent-a", Status: "unknown"})
	reg.SetHealth("agent-a", "online", "deepseek", "deepseek-chat")
	if reg.OnlineCount() != 1 {
		t.Fatalf("online count %d, want 1", reg.OnlineCount())
	}
	snap := reg.Snapshot()
	if len(snap) != 2 || snap[0].ID != "agent-a" || snap[1].ID != "agent-b" {
		t.Fatalf("snapshot not sorted by id: %v", snap)
	}
	card, ok := reg.Get("agent-a")
	if !ok || card.Status != "online" || card.Provider != "deepseek" || card.Model != "deepseek-chat" {
		t.Fatalf("health not recorded: %+v", card)
	}
}

func TestAggregateSingleAnswerReturnedDirectly(t *testing.T) {
	reg := NewRegistry(" ")
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}}, reg, fakeProvider{}, time.Second)
	answer, agg := h.aggregate(context.Background(), "task", []AgentResult{{AgentID: "a", Status: "ok", Answer: "solo"}})
	if !agg.OK || answer != "solo" || agg.Inputs != 1 {
		t.Fatalf("agg %+v answer %q", agg, answer)
	}
}

func TestAggregateFallsBackWhenProviderFails(t *testing.T) {
	reg := NewRegistry(" ")
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}}, reg, fakeProvider{err: errors.New("provider down")}, time.Second)
	results := []AgentResult{
		{AgentID: "a", Status: "ok", Answer: "answer-a", Tags: []string{"research"}},
		{AgentID: "b", Status: "ok", Answer: "answer-b", Tags: []string{"security"}},
		{AgentID: "c", Status: "failed"},
	}
	answer, agg := h.aggregate(context.Background(), "task", results)
	if agg.OK {
		t.Fatalf("aggregation should have fallen back: %+v", agg)
	}
	if agg.Error == " " {
		t.Fatal("aggregation error not recorded")
	}
	if agg.Inputs != 2 {
		t.Fatalf("inputs %d, want 2", agg.Inputs)
	}
	if !strings.Contains(answer, "answer-a") || !strings.Contains(answer, "answer-b") {
		t.Fatalf("fallback did not include both answers: %q", answer)
	}
}

func TestExecuteAgentTimeoutIsRecorded(t *testing.T) {
	reg := NewRegistry(" ")
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer slow.Close()
	reg.Register(AgentCard{ID: "agent-slow", Port: 9499, Endpoint: slow.URL, Tags: []string{"research"}, Status: "online"})
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}}, reg, fakeProvider{}, 60*time.Millisecond)
	card, _ := reg.Get("agent-slow")
	res := h.executeAgent(context.Background(), "task-x", Subtask{ID: "st-1", Tags: []string{"research"}}, card)
	if res.Status != "failed" {
		t.Fatalf("status %s, want failed", res.Status)
	}
	after, _ := reg.Get("agent-slow")
	if after.Timeouts != 1 {
		t.Fatalf("timeouts %d, want 1", after.Timeouts)
	}
	if after.Failure != 1 {
		t.Fatalf("failure %d, want 1", after.Failure)
	}
}

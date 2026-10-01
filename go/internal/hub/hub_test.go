package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/neural-hive/hive-node/internal/config"
	"github.com/neural-hive/hive-node/internal/llm"
)

type fakeProvider struct {
	reply string
	err   error
}

func (f fakeProvider) Name() string { return "fake" }

func (f fakeProvider) Complete(ctx context.Context, req llm.Request) (*llm.Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &llm.Result{Text: f.reply, Model: "fake-model", Provider: "fake"}, nil
}

func fakeAgent(t *testing.T, id string, tags []string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true,"provider":"fake","model":"fake-model"}`)
		case "/task":
			if status != 200 {
				http.Error(w, "agent failure", status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"agentId": id, "tags": tags, "answer": "answer from " + id,
				"model": "fake-model", "provider": "fake", "latencyMs": 5,
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestClassifySimple(t *testing.T) {
	cx := Classify("What is distributed consensus?")
	if cx.Level != "simple" {
		t.Fatalf("want simple, got %s score %d signals %v", cx.Level, cx.Score, cx.Signals)
	}
}

func TestClassifyComplex(t *testing.T) {
	cx := Classify("Analyze the architecture of a decentralized AI agent network, identify security risks, and propose an implementation strategy.")
	if cx.Level != "complex" {
		t.Fatalf("want complex, got %s score %d signals %v", cx.Level, cx.Score, cx.Signals)
	}
}

func TestDecomposeProducesTaggedSubtasks(t *testing.T) {
	q := "Analyze the architecture of a decentralized AI agent network, identify security risks, and propose an implementation strategy."
	cx := Classify(q)
	subs := Decompose(q, 3, cx.Domains)
	if len(subs) != 3 {
		t.Fatalf("want 3 subtasks, got %d", len(subs))
	}
	for _, s := range subs {
		if len(s.Tags) == 0 {
			t.Fatalf("subtask %s has no capability tags", s.ID)
		}
	}
}

func TestDecideHIVESimpleIsOne(t *testing.T) {
	reg := NewRegistry("")
	reg.Register(AgentCard{ID: "a", Status: "online"})
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}}, reg, fakeProvider{}, time.Second)
	cx := Classify("What is distributed consensus?")
	if got := h.decideHIVE(cx, nil, 0); got != 1 {
		t.Fatalf("simple hive = %d, want 1", got)
	}
}

func TestDecideHIVEComplexGrows(t *testing.T) {
	reg := NewRegistry("")
	for i := 0; i < 6; i++ {
		reg.Register(AgentCard{ID: fmt.Sprintf("agent-%02d", i+1), Status: "online"})
	}
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}}, reg, fakeProvider{}, time.Second)
	q := "Analyze the architecture, identify security risks, and propose a strategy"
	got := h.decideHIVE(Classify(q), SplitObjectives(q), 0)
	if got < 2 {
		t.Fatalf("complex hive = %d, want at least 2", got)
	}
}

func TestReputationAndRiskUpdate(t *testing.T) {
	reg := NewRegistry("")
	reg.Register(AgentCard{ID: "a", Status: "online"})
	c, _ := reg.Get("a")
	if c.Reputation != InitialReputation {
		t.Fatalf("initial reputation %v", c.Reputation)
	}
	reg.RecordResult("a", true, false, 100, "")
	c, _ = reg.Get("a")
	if c.Reputation <= InitialReputation {
		t.Fatalf("reputation did not rise: %v", c.Reputation)
	}
	if c.Risk >= InitialRisk {
		t.Fatalf("risk did not fall: %v", c.Risk)
	}
	if c.Success != 1 {
		t.Fatalf("success %d", c.Success)
	}
	reg.RecordResult("a", false, false, 200, "boom")
	c, _ = reg.Get("a")
	if c.Failure != 1 {
		t.Fatalf("failure %d", c.Failure)
	}
	if c.Risk <= InitialRisk {
		t.Fatalf("risk did not rise on failure: %v", c.Risk)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"
	reg := NewRegistry(path)
	reg.Register(AgentCard{ID: "a", Status: "online"})
	reg.RecordResult("a", true, false, 50, "")
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	reg2 := NewRegistry(path)
	reg2.Register(AgentCard{ID: "a", Status: "online"})
	if err := reg2.Load(); err != nil {
		t.Fatal(err)
	}
	c, _ := reg2.Get("a")
	if c.Success != 1 {
		t.Fatalf("success not persisted: %d", c.Success)
	}
	if c.Reputation <= InitialReputation {
		t.Fatalf("reputation not persisted: %v", c.Reputation)
	}
}

func TestRunSimpleTaskUsesOneAgent(t *testing.T) {
	reg := NewRegistry("")
	srvA := fakeAgent(t, "agent-01", []string{"research"}, 200)
	defer srvA.Close()
	reg.Register(AgentCard{ID: "agent-01", Port: 9401, Endpoint: srvA.URL, Tags: []string{"research", "summarization"}, Status: "online"})
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}, AggregatorModel: "fake"}, reg, fakeProvider{reply: "agg"}, 5*time.Second)
	h.HealthCheck(context.Background())
	res, err := h.Run(context.Background(), "What is distributed consensus?", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.HIVE != 1 {
		t.Fatalf("hive %d, want 1", res.HIVE)
	}
	if res.Complexity.Level != "simple" {
		t.Fatalf("level %s", res.Complexity.Level)
	}
	if res.Answer != "answer from agent-01" {
		t.Fatalf("answer %q", res.Answer)
	}
	if len(res.AgentResults) != 1 {
		t.Fatalf("agent results %d, want 1", len(res.AgentResults))
	}
	if res.AnswerHash == "" {
		t.Fatal("answer hash missing")
	}
}

func TestRunComplexTaskUsesMultipleAgents(t *testing.T) {
	reg := NewRegistry("")
	srvA := fakeAgent(t, "agent-01", []string{"research"}, 200)
	srvB := fakeAgent(t, "agent-02", []string{"security", "analysis"}, 200)
	srvC := fakeAgent(t, "agent-03", []string{"architecture", "planning"}, 200)
	defer srvA.Close()
	defer srvB.Close()
	defer srvC.Close()
	reg.Register(AgentCard{ID: "agent-01", Port: 9401, Endpoint: srvA.URL, Tags: []string{"research"}, Status: "online"})
	reg.Register(AgentCard{ID: "agent-02", Port: 9402, Endpoint: srvB.URL, Tags: []string{"security", "analysis"}, Status: "online"})
	reg.Register(AgentCard{ID: "agent-03", Port: 9403, Endpoint: srvC.URL, Tags: []string{"architecture", "planning"}, Status: "online"})
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}, AggregatorModel: "fake"}, reg, fakeProvider{reply: "aggregated final answer"}, 5*time.Second)
	h.HealthCheck(context.Background())
	q := "Analyze the architecture of a decentralized AI agent network, identify security risks, and propose an implementation strategy."
	res, err := h.Run(context.Background(), q, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Complexity.Level != "complex" {
		t.Fatalf("level %s", res.Complexity.Level)
	}
	if res.HIVE != 2 {
		t.Fatalf("hive %d, want 2", res.HIVE)
	}
	if len(res.AgentResults) != 2 {
		t.Fatalf("agent results %d, want 2", len(res.AgentResults))
	}
	if res.Answer != "aggregated final answer" {
		t.Fatalf("answer %q", res.Answer)
	}
	used := map[string]bool{}
	for _, r := range res.AgentResults {
		used[r.AgentID] = true
	}
	if len(used) != 2 {
		t.Fatalf("distinct agents %d, want 2", len(used))
	}
	for _, r := range res.AgentResults {
		if r.ReputationAfter <= r.ReputationBefore {
			t.Fatalf("reputation did not rise for %s", r.AgentID)
		}
	}
}

func TestPartialFailureIsRecorded(t *testing.T) {
	reg := NewRegistry("")
	srvGood := fakeAgent(t, "agent-01", []string{"research"}, 200)
	srvBad := fakeAgent(t, "agent-02", []string{"security"}, 500)
	defer srvGood.Close()
	defer srvBad.Close()
	reg.Register(AgentCard{ID: "agent-01", Port: 9401, Endpoint: srvGood.URL, Tags: []string{"research", "analysis"}, Status: "online"})
	reg.Register(AgentCard{ID: "agent-02", Port: 9402, Endpoint: srvBad.URL, Tags: []string{"security", "analysis"}, Status: "online"})
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 10}, AggregatorModel: "fake"}, reg, fakeProvider{reply: "agg"}, 5*time.Second)
	h.HealthCheck(context.Background())
	q := "Analyze the architecture, identify security risks, and propose a strategy"
	res, err := h.Run(context.Background(), q, 2)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	ok := 0
	for _, r := range res.AgentResults {
		if r.Status == "failed" {
			failed++
		}
		if r.Status == "ok" {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("ok results %d, want 1", ok)
	}
	if failed != 1 {
		t.Fatalf("failed results %d, want 1", failed)
	}
	if len(res.Errors) == 0 {
		t.Fatal("partial failure was not recorded")
	}
	badCard, _ := reg.Get("agent-02")
	if badCard.Failure != 1 {
		t.Fatalf("failed agent not penalised: %d", badCard.Failure)
	}
}

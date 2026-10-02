package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/neural-hive/hive-node/internal/config"
)

// TestDecideHIVEClampsByRuntimeCap checks the runtime max-agents cap bounds decideHIVE and that
// the cap is clamped to [1, HIVE_MAX].
func TestDecideHIVEClampsByRuntimeCap(t *testing.T) {
	reg := NewRegistry(" ")
	for i := 1; i <= 8; i++ {
		reg.Register(AgentCard{ID: "agent-" + string(rune(48+i)), Port: 9400 + i, Tags: []string{"research"}, Status: "online"})
	}
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 8}}, reg, fakeProvider{}, time.Second)
	if got := h.MaxAgents(); got != 8 {
		t.Fatalf("default cap = %d, want 8", got)
	}
	cx := Complexity{Level: "complex"}
	objs := []string{"a", "b", "c", "d", "e", "f"}
	if got := h.decideHIVE(cx, objs, 0); got != 6 {
		t.Fatalf("hive without cap = %d, want 6", got)
	}
	if got := h.SetMaxAgents(2); got != 2 {
		t.Fatalf("SetMaxAgents(2) = %d, want 2", got)
	}
	if got := h.decideHIVE(cx, objs, 0); got != 2 {
		t.Fatalf("hive with cap 2 = %d, want 2", got)
	}
	if got := h.SetMaxAgents(99); got != 8 {
		t.Fatalf("SetMaxAgents(99) = %d, want 8 (ceiling)", got)
	}
	if got := h.SetMaxAgents(0); got != 1 {
		t.Fatalf("SetMaxAgents(0) = %d, want 1", got)
	}
}

// TestAgentLimitEndpoint checks GET/POST /agents/limit updates the runtime cap and reports it.
func TestAgentLimitEndpoint(t *testing.T) {
	reg := NewRegistry(" ")
	for i := 1; i <= 5; i++ {
		reg.Register(AgentCard{ID: "agent-" + string(rune(48+i)), Port: 9400 + i, Tags: []string{"research"}, Status: "online"})
	}
	h := NewWithProvider(config.Hub{HIVE: config.HIVEPolicy{Default: 1, Complex: 2, Max: 5}}, reg, fakeProvider{}, time.Second)
	srv := httptest.NewServer(h.Handler(""))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/agents/limit", "application/json", strings.NewReader("{\"max\":3}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		MaxAgents int `json:"maxAgents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.MaxAgents != 3 {
		t.Fatalf("POST /agents/limit maxAgents = %d, want 3", out.MaxAgents)
	}
	if got := h.MaxAgents(); got != 3 {
		t.Fatalf("hub cap = %d, want 3", got)
	}
	g, err := http.Get(srv.URL + "/agents/limit")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Body.Close()
	var out2 struct {
		Ceiling int `json:"maxAgentsCeiling"`
	}
	if err := json.NewDecoder(g.Body).Decode(&out2); err != nil {
		t.Fatal(err)
	}
	if out2.Ceiling != 5 {
		t.Fatalf("ceiling = %d, want 5", out2.Ceiling)
	}
}

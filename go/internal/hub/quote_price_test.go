package hub

import (
	"testing"
)

// TestQuoteUsesAgentPricesNotConstant checks that a quote is priced from the advertised prices of
// the agents the request will actually use, not a flat per-agent constant: with a 3-HIVE and a
// 5-HIVE agent and a two-agent complex task, the quote must be 8 HIVE even though the configured
// per-agent price is 1.
func TestQuoteUsesAgentPricesNotConstant(t *testing.T) {
	h := payTestHub(true, "")
	h.Registry.Register(AgentCard{ID: "a1", Index: 1, Tags: []string{"research", "analysis", "coding", "security", "planning", "reasoning"}, Status: "online", PriceHive: 3})
	h.Registry.Register(AgentCard{ID: "a2", Index: 2, Tags: []string{"research", "analysis", "coding", "security", "planning", "reasoning"}, Status: "online", PriceHive: 5})
	q := h.Quote("Analyze the architecture, identify security risks, and propose a strategy.", 0, AlgConfig{})
	got := FormatHive(q.CostWei)
	if got == "" || got == "0" {
		t.Fatalf("quote cost must be positive, got %q", got)
	}
	// Two subtasks route to the two agents (3 + 5); the flat per-agent constant would be 2.
	if got != "8" {
		t.Fatalf("quote cost = %q, want 8 (sum of agent prices 3+5, hive=%d)", got, q.HIVE)
	}
}

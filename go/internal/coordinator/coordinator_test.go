package coordinator

import (
	"math/big"
	"testing"
)

// TestDeepSeekAgentSpecMatchesAnswerEndpoint proves the DeepSeek agent is registered with
// exactly the identity, skills and endpoint the coordinator later designates as the answerer,
// which is what lets ensureEndpoint swap it onto the final step committee.
func TestDeepSeekAgentSpecMatchesAnswerEndpoint(t *testing.T) {
	price := big.NewInt(123)
	spec := DeepSeekAgentSpec(6, "http://127.0.0.1:9107", price)
	if spec.Index != 6 {
		t.Fatalf("index = %d", spec.Index)
	}
	if spec.Endpoint != "http://127.0.0.1:9107" {
		t.Fatalf("endpoint = %q", spec.Endpoint)
	}
	if spec.Price.Cmp(price) != 0 {
		t.Fatalf("price not preserved")
	}
	want := map[string]bool{"explain": true, "summarization": true, "reasoning": true}
	if len(spec.Skills) != len(want) {
		t.Fatalf("skills = %v", spec.Skills)
	}
	for _, s := range spec.Skills {
		if !want[s] {
			t.Fatalf("unexpected skill %q", s)
		}
	}
}

// TestDeepSeekAgentIndexIsDistinct keeps the DeepSeek agent off the mock agents on-chain keys.
func TestDeepSeekAgentIndexIsDistinct(t *testing.T) {
	if DeepSeekAgentSpec(6, "e", big.NewInt(0)).Index == DefaultAgentSpecs(nil, big.NewInt(0))[0].Index {
		t.Fatalf("the DeepSeek agent must not reuse a mock agent index")
	}
}

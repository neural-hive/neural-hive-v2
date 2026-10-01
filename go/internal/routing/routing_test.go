package routing

import (
	"testing"

	"github.com/neural-hive/hive-node/internal/capability"
	"github.com/neural-hive/hive-node/internal/hnsw"
)

func view(addr string, skills []string, rep, price, load float64) AgentView {
	return AgentView{
		Address:    addr,
		Capability: capability.FromSkills(skills),
		Reputation: rep,
		Price:      price,
		MaxPrice:   0.1,
		Load:       load,
		Endpoint:   "http://" + addr,
	}
}

func TestGatePrefersReputationAndCheaperPrice(t *testing.T) {
	task := capability.FromSkills([]string{"risk-scoring"})
	good := view("0xaa", []string{"risk-scoring"}, 1.0, 0.01, 0.0)
	bad := view("0xbb", []string{"risk-scoring"}, 0.1, 0.09, 0.9)
	_, goodScore := Gate(DefaultGateWeights(), task, good.Capability, good.Reputation, good.Price, good.MaxPrice, good.Load)
	_, badScore := Gate(DefaultGateWeights(), task, bad.Capability, bad.Reputation, bad.Price, bad.MaxPrice, bad.Load)
	if goodScore <= badScore {
		t.Fatalf("trusted, cheap and idle agents must score higher: %f vs %f", goodScore, badScore)
	}
}

func TestRouteCandidatesRanksAndTruncates(t *testing.T) {
	idx := hnsw.New(8, 32, 32, capability.Dim, 1)
	agents := map[string]AgentView{}
	for i := 0; i < 10; i++ {
		a := view("0xa"+string(rune(int('0')+i)), []string{"summarization"}, 0.5, 0.02+float64(i)*0.001, 0.0)
		agents[a.Address] = a
		idx.Insert(a.Address, a.Capability)
	}
	out := RouteCandidates(idx, agents, capability.FromSkills([]string{"summarization"}), 16, 4)
	if len(out) != 4 {
		t.Fatalf("expected the top 4 candidates, got %d", len(out))
	}
	for i := 1; i < len(out); i++ {
		if out[i-1].Score < out[i].Score {
			t.Fatalf("candidates must be ordered by descending score")
		}
	}
}

func TestSelfishRoutePicksLowestPerceivedCost(t *testing.T) {
	s := NewSelfishRouter(1.5)
	cands := []Scored{
		{AgentView: view("0x1", nil, 0.5, 0.05, 0.4)},
		{AgentView: view("0x2", nil, 0.1, 0.02, 0.0)},
	}
	best, ok := s.Route(cands)
	if !ok || best.Address != "0x2" {
		t.Fatalf("expected the lowest load+price agent, got %s", best.Address)
	}
	if _, ok := s.Route(nil); ok {
		t.Fatalf("routing with no candidates must report false")
	}
}

func TestSelfishRouteTieBreaksOnReputation(t *testing.T) {
	s := NewSelfishRouter(1.5)
	cands := []Scored{
		{AgentView: view("0x1", nil, 0.2, 0.02, 0.0)},
		{AgentView: view("0x2", nil, 0.9, 0.02, 0.0)},
	}
	best, _ := s.Route(cands)
	if best.Address != "0x2" {
		t.Fatalf("a cost tie must break towards higher reputation, got %s", best.Address)
	}
}

func TestPriceOfAnarchyWatchdogTogglesMode(t *testing.T) {
	s := NewSelfishRouter(1.5)
	if s.CurrentMode() != ModeSelfish {
		t.Fatalf("the router must start in selfish mode")
	}
	s.ObserveWait(1.05, 1.0)
	if s.CurrentMode() != ModeSelfish {
		t.Fatalf("a price of anarchy near 1 must stay selfish")
	}
	s.ObserveWait(10, 1)
	if s.CurrentMode() != ModeBandit || !s.ShouldUseBandit() {
		t.Fatalf("a price of anarchy above the threshold must switch to the bandit, got %s", s.CurrentMode())
	}
	for i := 0; i < 100; i++ {
		s.ObserveWait(1, 1)
	}
	if s.CurrentMode() != ModeSelfish {
		t.Fatalf("the router must fall back to selfish once the network settles, got %s", s.CurrentMode())
	}
}

func TestLinUCBSelectsAllArmsAndLearns(t *testing.T) {
	l := NewLinUCB(3, 0.2)
	x := []float64{1, 0, 0}
	arms := []string{"alpha", "beta"}
	if first := l.Select(x, arms); first == "" {
		t.Fatalf("linucb must pick an arm")
	}
	if len(l.Arms()) != 2 {
		t.Fatalf("both arms should be initialised after scoring, got %d", len(l.Arms()))
	}
	for i := 0; i < 50; i++ {
		l.Update("beta", x, 1.0)
	}
	if got := l.Select(x, arms); got != "beta" {
		t.Fatalf("the consistently rewarded arm must dominate, got %s", got)
	}
}

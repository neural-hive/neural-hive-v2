// Package routing implements Neural Hive task routing: semantic candidate retrieval plus a
// learned gate (4.2.1), selfish routing with a price-of-anarchy watchdog (4.2.2), and the LinUCB
// contextual-bandit fallback (4.2.3).
package routing

import (
	"sort"

	"github.com/neural-hive/hive-node/internal/capability"
	"github.com/neural-hive/hive-node/internal/hnsw"
)

// AgentView is the routing-relevant snapshot of a registered agent.
type AgentView struct {
	Address    string
	Capability capability.Vector
	Reputation float64
	Price      float64
	MaxPrice   float64
	Load       float64
	Stake      float64
	Endpoint   string
}

// Scored is a ranked candidate.
type Scored struct {
	AgentView
	Similarity float64
	Score      float64
}

// GateWeights parameterise the learned scoring step of ROUTE_CANDIDATES.
type GateWeights struct {
	Similarity float64
	Reputation float64
	Price      float64
	Load       float64
}

// DefaultGateWeights are the prototype weights: task/agent match dominates, then trust, then cost and load.
func DefaultGateWeights() GateWeights {
	return GateWeights{Similarity: 0.45, Reputation: 0.20, Price: 0.20, Load: 0.15}
}

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// Gate is the learned scoring step of ROUTE_CANDIDATES: it blends task/agent semantic match with
// reputation, price and current load, so routing is not similarity-only.
func Gate(w GateWeights, task, agent capability.Vector, reputation, price, maxPrice, load float64) (float64, float64) {
	sim := capability.Cosine(task, agent)
	priceTerm := 1.0
	if maxPrice > 0 {
		priceTerm = 1.0 - clamp(price/maxPrice, 0, 1)
	}
	loadTerm := 1.0 - clamp(load, 0, 1)
	rep := clamp(reputation, 0, 1)
	score := w.Similarity*((sim+1)/2) + w.Reputation*rep + w.Price*priceTerm + w.Load*loadTerm
	return sim, score
}

// ScoreCandidates applies the gate to a hit list and returns the top N.
func ScoreCandidates(hits []hnsw.Result, agents map[string]AgentView, task capability.Vector, w GateWeights, topN int) []Scored {
	maxPrice := 0.0
	for _, h := range hits {
		if a, ok := agents[h.ID]; ok && a.Price > maxPrice {
			maxPrice = a.Price
		}
	}
	out := make([]Scored, 0, len(hits))
	for _, h := range hits {
		a, ok := agents[h.ID]
		if !ok {
			continue
		}
		sim, score := Gate(w, task, a.Capability, a.Reputation, a.Price, maxPrice, a.Load)
		out = append(out, Scored{AgentView: a, Similarity: sim, Score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Address < out[j].Address
		}
		return out[i].Score > out[j].Score
	})
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out
}

// RouteCandidates is the ROUTE_CANDIDATES procedure: HNSW retrieval followed by gate re-ranking.
func RouteCandidates(idx *hnsw.Index, agents map[string]AgentView, task capability.Vector, searchK, topN int) []Scored {
	hits := idx.Search(task, searchK)
	return ScoreCandidates(hits, agents, task, DefaultGateWeights(), topN)
}

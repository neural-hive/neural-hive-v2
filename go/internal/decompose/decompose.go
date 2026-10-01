// Package decompose turns a request into a step graph and schedules it by critical path
// (proposal 4.2.4).
//
// A request such as check the contract, summarize the risk, suggest a fix is not one task: the
// second and third steps depend on the first. DECOMPOSE builds that graph, TOPOLOGICAL_SORT orders
// it, and BACKTRACK_LONGEST_PATH finds the chain that actually decides total latency, so the
// fastest agents can be pointed at it.
package decompose

import (
	"fmt"
	"sort"
	"strings"
)

// Step is one node of the request graph.
type Step struct {
	ID       string
	Kind     string
	Skills   []string
	Duration float64
	Deps     []string
	Numeric  bool
	MaxPrice float64
	Critical bool
}

// Schedule is the timing computed for a step.
type Schedule struct {
	EarliestStart  float64
	EarliestFinish float64
}

// Graph is a request step graph.
type Graph struct {
	Request string
	Steps   []Step
}

// Step returns the step with the given id.
func (g *Graph) Step(id string) (Step, bool) {
	for _, s := range g.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

// TopologicalSort returns a valid execution order (Kahn algorithm, deterministic tie-breaking).
func (g *Graph) TopologicalSort() ([]string, error) {
	indeg := map[string]int{}
	adj := map[string][]string{}
	ids := []string{}
	for _, s := range g.Steps {
		indeg[s.ID] = 0
		ids = append(ids, s.ID)
	}
	for _, s := range g.Steps {
		for _, d := range s.Deps {
			if _, ok := indeg[d]; !ok {
				return nil, fmt.Errorf("step %s depends on unknown step %s", s.ID, d)
			}
			adj[d] = append(adj[d], s.ID)
			indeg[s.ID]++
		}
	}
	sort.Strings(ids)
	ready := []string{}
	for _, id := range ids {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	order := []string{}
	for len(ready) > 0 {
		sort.Strings(ready)
		cur := ready[0]
		ready = ready[1:]
		order = append(order, cur)
		next := append([]string{}, adj[cur]...)
		sort.Strings(next)
		for _, n := range next {
			indeg[n]--
			if indeg[n] == 0 {
				ready = append(ready, n)
			}
		}
	}
	if len(order) != len(g.Steps) {
		return nil, fmt.Errorf("cycle detected in step graph")
	}
	return order, nil
}

// Schedule computes earliest start/finish for every step in topological order.
func (g *Graph) Schedule() (map[string]Schedule, []string, error) {
	order, err := g.TopologicalSort()
	if err != nil {
		return nil, nil, err
	}
	sch := map[string]Schedule{}
	for _, id := range order {
		s, _ := g.Step(id)
		start := 0.0
		for _, d := range s.Deps {
			if sch[d].EarliestFinish > start {
				start = sch[d].EarliestFinish
			}
		}
		sch[id] = Schedule{EarliestStart: start, EarliestFinish: start + s.Duration}
	}
	return sch, order, nil
}

// CriticalPath finds the slowest chain of steps (BACKTRACK_LONGEST_PATH) and marks those steps.
func (g *Graph) CriticalPath() ([]string, float64, error) {
	sch, _, err := g.Schedule()
	if err != nil {
		return nil, 0, err
	}
	endID := ""
	best := -1.0
	for _, s := range g.Steps {
		if sch[s.ID].EarliestFinish > best {
			best = sch[s.ID].EarliestFinish
			endID = s.ID
		}
	}
	if endID == "" {
		return nil, 0, nil
	}
	path := []string{}
	cur := endID
	for cur != "" {
		path = append([]string{cur}, path...)
		s, _ := g.Step(cur)
		prev := ""
		prevFinish := -1.0
		for _, d := range s.Deps {
			if sch[d].EarliestFinish > prevFinish {
				prevFinish = sch[d].EarliestFinish
				prev = d
			}
		}
		cur = prev
	}
	for i := range g.Steps {
		g.Steps[i].Critical = false
		for _, p := range path {
			if g.Steps[i].ID == p {
				g.Steps[i].Critical = true
			}
		}
	}
	return path, best, nil
}

// Decompose builds a step graph from a request description. The prototype uses deterministic
// templates keyed on intent, which keeps integration tests reproducible.
func Decompose(request string) *Graph {
	r := request
	switch {
	case containsAny(r, "risk", "loan", "flag"):
		return &Graph{Request: request, Steps: []Step{
			{ID: "s1", Kind: "risk-scoring", Skills: []string{"risk-scoring"}, Duration: 0.4, Numeric: true, MaxPrice: 0.05},
			{ID: "s2", Kind: "explain", Skills: []string{"explain", "summarization"}, Duration: 0.5, Deps: []string{"s1"}, MaxPrice: 0.05},
			{ID: "s3", Kind: "compliance-flag", Skills: []string{"compliance"}, Duration: 0.3, Deps: []string{"s2"}, MaxPrice: 0.05},
		}}
	case containsAny(r, "trade", "liquidity", "rebalance", "market"):
		return &Graph{Request: request, Steps: []Step{
			{ID: "s1", Kind: "market-data", Skills: []string{"market-data"}, Duration: 0.3, Numeric: true, MaxPrice: 0.05},
			{ID: "s2", Kind: "trading-signal", Skills: []string{"trading-signal", "defi-strategy"}, Duration: 0.6, Deps: []string{"s1"}, Numeric: true, MaxPrice: 0.08},
			{ID: "s3", Kind: "liquidity-management", Skills: []string{"liquidity-management"}, Duration: 0.4, Deps: []string{"s2"}, Numeric: true, MaxPrice: 0.08},
		}}
	case containsAny(r, "audit", "contract", "review"):
		return &Graph{Request: request, Steps: []Step{
			{ID: "s1", Kind: "code-review", Skills: []string{"code-review"}, Duration: 0.5, MaxPrice: 0.05},
			{ID: "s2", Kind: "smart-contract-audit", Skills: []string{"smart-contract-audit"}, Duration: 0.7, Deps: []string{"s1"}, MaxPrice: 0.08},
			{ID: "s3", Kind: "summarization", Skills: []string{"summarization"}, Duration: 0.3, Deps: []string{"s2"}, MaxPrice: 0.05},
		}}
	case containsAny(r, "moderate", "sentiment", "anomaly"):
		return &Graph{Request: request, Steps: []Step{
			{ID: "s1", Kind: "sentiment", Skills: []string{"sentiment"}, Duration: 0.3, MaxPrice: 0.04},
			{ID: "s2", Kind: "moderation", Skills: []string{"moderation"}, Duration: 0.4, Deps: []string{"s1"}, MaxPrice: 0.04},
		}}
	default:
		return &Graph{Request: request, Steps: []Step{
			{ID: "s1", Kind: "summarization", Skills: []string{"summarization"}, Duration: 0.4, MaxPrice: 0.05},
		}}
	}
}

func containsAny(s string, subs ...string) bool {
	ls := strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(ls, sub) {
			return true
		}
	}
	return false
}

// Allocation records which critical-path steps should receive the fastest agents.
type Allocation struct {
	StepID       string
	Priority     int
	CriticalPath bool
}

// AllocateFastestAgents marks critical-path steps for lowest-latency agents (ALLOCATE_FASTEST_AGENTS).
// Critical steps get priority 0 (most urgent); the rest follow in topological order.
func (g *Graph) AllocateFastestAgents() []Allocation {
	path, _, err := g.CriticalPath()
	if err != nil {
		return nil
	}
	onPath := map[string]bool{}
	for _, p := range path {
		onPath[p] = true
	}
	order, err := g.TopologicalSort()
	if err != nil {
		return nil
	}
	out := make([]Allocation, 0, len(order))
	prio := 0
	for _, id := range order {
		if onPath[id] {
			out = append(out, Allocation{StepID: id, Priority: 0, CriticalPath: true})
			continue
		}
		prio++
		out = append(out, Allocation{StepID: id, Priority: prio, CriticalPath: false})
	}
	return out
}

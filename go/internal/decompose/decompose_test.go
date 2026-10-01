package decompose

import (
	"testing"
)

func TestDecomposeRiskRequestBuildsADependencyGraph(t *testing.T) {
	g := Decompose("check this loan risk, explain it and flag it for review")
	if len(g.Steps) != 3 {
		t.Fatalf("expected a three step graph, got %d", len(g.Steps))
	}
	if len(g.Steps[1].Deps) != 1 || g.Steps[1].Deps[0] != "s1" {
		t.Fatalf("step s2 must depend on s1")
	}
	order, err := g.TopologicalSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if !(pos["s1"] < pos["s2"] && pos["s2"] < pos["s3"]) {
		t.Fatalf("dependencies must be scheduled first, got %v", order)
	}
}

func TestCriticalPathMarksTheLongestChain(t *testing.T) {
	g := Decompose("audit the contract and review it")
	path, total, err := g.CriticalPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(path) != 3 {
		t.Fatalf("the whole chain is the critical path, got %v", path)
	}
	if total <= 0 {
		t.Fatalf("the critical path duration must be positive")
	}
	n := 0
	for _, s := range g.Steps {
		if s.Critical {
			n++
		}
	}
	if n != len(path) {
		t.Fatalf("every step on the critical path must be flagged, got %d", n)
	}
}

func TestAllocateFastestAgentsPrioritisesTheCriticalPath(t *testing.T) {
	g := Decompose("check this loan risk")
	allocs := g.AllocateFastestAgents()
	if len(allocs) != len(g.Steps) {
		t.Fatalf("every step needs an allocation, got %d", len(allocs))
	}
	if !allocs[0].CriticalPath || allocs[0].Priority != 0 {
		t.Fatalf("critical path steps must get the highest priority")
	}
}

func TestTopologicalSortDetectsACycle(t *testing.T) {
	g := &Graph{Steps: []Step{{ID: "a", Deps: []string{"b"}}, {ID: "b", Deps: []string{"a"}}}}
	if _, err := g.TopologicalSort(); err == nil {
		t.Fatalf("a cycle must be reported as an error")
	}
}

func TestUnknownIntentFallsBackToASingleStep(t *testing.T) {
	g := Decompose("hello there")
	if len(g.Steps) != 1 {
		t.Fatalf("an unknown intent must produce one step, got %d", len(g.Steps))
	}
	if g.Steps[0].Kind != "summarization" {
		t.Fatalf("expected the summarization fallback, got %s", g.Steps[0].Kind)
	}
}

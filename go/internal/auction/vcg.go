// Package auction implements the VCG payment rule, proposal 4.2.7.
package auction

import "sort"

// Bid is one agent offer for a step.
type Bid struct {
	Agent string
	Cost  float64
}

// Allocation is the VCG outcome for a single item.
type Allocation struct {
	Winner     string
	Payment    float64
	RunnerUp   string
	RunnerCost float64
}

// VCGAllocate implements the simplified single-item VCG rule from the proposal: the winner is the
// lowest bid and is paid the second-lowest bid rather than its own. That makes truthful bidding
// the dominant strategy, so agents have no reason to inflate or undercut their real cost.
func VCGAllocate(bids []Bid) Allocation {
	if len(bids) == 0 {
		return Allocation{}
	}
	sorted := make([]Bid, len(bids))
	copy(sorted, bids)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Cost == sorted[j].Cost {
			return sorted[i].Agent < sorted[j].Agent
		}
		return sorted[i].Cost < sorted[j].Cost
	})
	winner := sorted[0]
	if len(sorted) == 1 {
		return Allocation{Winner: winner.Agent, Payment: winner.Cost}
	}
	runner := sorted[1]
	return Allocation{Winner: winner.Agent, Payment: runner.Cost, RunnerUp: runner.Agent, RunnerCost: runner.Cost}
}

// VCGAllocateEligible runs the rule over the subset of bidders whose answer matched the Krum
// consensus. Agents that produced a wrong answer are not eligible for allocation at all.
func VCGAllocateEligible(bids []Bid, eligible map[string]bool) Allocation {
	subset := make([]Bid, 0, len(bids))
	for _, b := range bids {
		if eligible[b.Agent] {
			subset = append(subset, b)
		}
	}
	return VCGAllocate(subset)
}

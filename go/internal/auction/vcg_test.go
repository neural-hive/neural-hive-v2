package auction

import (
	"testing"
)

func TestVCGSingleBidder(t *testing.T) {
	a := VCGAllocate([]Bid{{Agent: "solo", Cost: 0.05}})
	if a.Winner != "solo" || a.Payment != 0.05 {
		t.Fatalf("a single bidder wins and is paid its own bid: %+v", a)
	}
	if empty := VCGAllocate(nil); empty.Winner != "" {
		t.Fatalf("no bids must produce an empty allocation")
	}
}

func TestVCGPaysTheSecondLowestBid(t *testing.T) {
	a := VCGAllocate([]Bid{{Agent: "x", Cost: 0.09}, {Agent: "y", Cost: 0.02}, {Agent: "z", Cost: 0.05}})
	if a.Winner != "y" {
		t.Fatalf("the lowest bidder must win, got %s", a.Winner)
	}
	if a.Payment != 0.05 {
		t.Fatalf("the winner must be paid the second-lowest bid, got %f", a.Payment)
	}
	if a.RunnerUp != "z" {
		t.Fatalf("expected z as the runner up, got %s", a.RunnerUp)
	}
}

func TestVCGTieBreakIsDeterministic(t *testing.T) {
	a := VCGAllocate([]Bid{{Agent: "b", Cost: 1}, {Agent: "a", Cost: 1}})
	if a.Winner != "a" {
		t.Fatalf("a cost tie must break on the agent address, got %s", a.Winner)
	}
}

func TestVCGEligibleIgnoresWrongAnswers(t *testing.T) {
	bids := []Bid{{Agent: "honest", Cost: 0.03}, {Agent: "liar", Cost: 0.01}}
	a := VCGAllocateEligible(bids, map[string]bool{"honest": true})
	if a.Winner != "honest" {
		t.Fatalf("an ineligible bidder must never be allocated, got %s", a.Winner)
	}
	if a.Payment != 0.03 {
		t.Fatalf("a lone eligible bidder is paid its own bid, got %f", a.Payment)
	}
}

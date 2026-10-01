// Selfish routing and the price-of-anarchy watchdog (proposal 4.2.2).
//
// Most tasks simply go to the qualified agent that looks best locally: perceived cost is the
// agent advertised load plus its price, and the task picks the argmin with no central decision.
// A routing game of this shape settles at a Nash equilibrium, but that equilibrium can be worse
// than a centrally planned allocation (Roughgarden & Tardos, 2002). We therefore track the
// realised price of anarchy and fail over to the LinUCB bandit (4.2.3) when it crosses a
// threshold, switching back once the network settles.
package routing

import "sync"

// Mode is the active routing regime.
type Mode string

const (
	// ModeSelfish is the cheap, decentralised default.
	ModeSelfish Mode = "selfish"
	// ModeBandit is the slower but smarter LinUCB fallback.
	ModeBandit Mode = "bandit"
)

// SelfishRouter implements SELFISH_ROUTE and the poa_estimate watchdog.
type SelfishRouter struct {
	mu          sync.Mutex
	Threshold   float64
	EmaAlpha    float64
	avgWait     float64
	optimalWait float64
	samples     int
	mode        Mode
}

// NewSelfishRouter creates a router with the given price-of-anarchy threshold.
func NewSelfishRouter(threshold float64) *SelfishRouter {
	return &SelfishRouter{Threshold: threshold, EmaAlpha: 0.2, mode: ModeSelfish}
}

// PerceivedCost is advertised_load + price, exactly as in the SELFISH_ROUTE pseudocode.
func (s *SelfishRouter) PerceivedCost(a AgentView) float64 { return a.Load + a.Price }

// Route picks the candidate with the lowest perceived cost. Ties break on reputation then address,
// which keeps selection deterministic for reproducible tests.
func (s *SelfishRouter) Route(cands []Scored) (Scored, bool) {
	if len(cands) == 0 {
		return Scored{}, false
	}
	best := cands[0]
	bestCost := s.PerceivedCost(best.AgentView)
	for _, c := range cands[1:] {
		cost := s.PerceivedCost(c.AgentView)
		if cost < bestCost || (cost == bestCost && c.Reputation > best.Reputation) ||
			(cost == bestCost && c.Reputation == best.Reputation && c.Address < best.Address) {
			best = c
			bestCost = cost
		}
	}
	return best, true
}

// ObserveWait feeds the watchdog: actual measured wait versus an estimate of the best achievable wait.
func (s *SelfishRouter) ObserveWait(actual, optimal float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.samples == 0 {
		s.avgWait = actual
		s.optimalWait = optimal
	} else {
		s.avgWait = s.EmaAlpha*actual + (1-s.EmaAlpha)*s.avgWait
		s.optimalWait = s.EmaAlpha*optimal + (1-s.EmaAlpha)*s.optimalWait
	}
	s.samples++
	s.updateModeLocked()
}

// PriceOfAnarchy is avg_actual_wait / estimated_optimal_wait. 1.0 means selfish routing is optimal.
func (s *SelfishRouter) PriceOfAnarchy() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.poaLocked()
}

func (s *SelfishRouter) poaLocked() float64 {
	if s.optimalWait <= 0 || s.samples == 0 {
		return 1.0
	}
	return s.avgWait / s.optimalWait
}

func (s *SelfishRouter) updateModeLocked() {
	if s.poaLocked() > s.Threshold {
		s.mode = ModeBandit
	} else {
		s.mode = ModeSelfish
	}
}

// CurrentMode returns the active routing regime.
func (s *SelfishRouter) CurrentMode() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// ShouldUseBandit reports whether the watchdog has tripped.
func (s *SelfishRouter) ShouldUseBandit() bool { return s.CurrentMode() == ModeBandit }

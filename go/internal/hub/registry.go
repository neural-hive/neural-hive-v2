package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Reputation and risk are updated from the real observed outcome of every execution.
//
// Reputation (r) rewards success and decays on failure, moving asymptotically toward 1 or 0:
//   success: r <- r + AlphaReputation*(1-r)
//   failure: r <- r - AlphaReputation*r
// Risk (k) is the Hub''s estimate that the next call to an agent fails, an exponential moving
// average of the failure signal:
//   success: k <- k - BetaRisk*k
//   failure: k <- k + BetaRisk*(1-k)
// Both live in [0,1] and start neutral; a brand new agent has no invented history.
const (
	AlphaReputation   = 0.15
	BetaRisk          = 0.2
	InitialReputation = 0.5
	InitialRisk       = 0.1
)

// Registry is the Hub source of truth for the available workers.
type Registry struct {
	mu        sync.RWMutex
	cards     map[string]*AgentCard
	order     []string
	stateFile string
}

type persistedCard struct {
	ID             string  `json:"id"`
	Reputation     float64 `json:"reputation"`
	Risk           float64 `json:"risk"`
	Success        int     `json:"success"`
	Failure        int     `json:"failure"`
	Timeouts       int     `json:"timeouts"`
	Executions     int     `json:"executions"`
	TotalLatencyMs int64   `json:"totalLatencyMs"`
	AvgLatencyMs   int64   `json:"avgLatencyMs"`
	LastSeen       string  `json:"lastSeen"`
	LastError      string  `json:"lastError"`
}

type persisted struct {
	Cards []persistedCard `json:"cards"`
}

// NewRegistry builds an empty registry; stateFile may be empty to disable persistence.
func NewRegistry(stateFile string) *Registry {
	return &Registry{cards: map[string]*AgentCard{}, order: []string{}, stateFile: stateFile}
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// Register adds or refreshes the card for a configured agent. It is idempotent and preserves any
// metrics already accumulated for that agent id.
func (r *Registry) Register(card AgentCard) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.cards[card.ID]; ok {
		existing.Index = card.Index
		existing.Port = card.Port
		existing.Endpoint = card.Endpoint
		existing.Tags = append([]string{}, card.Tags...)
		existing.Model = card.Model
		if card.Provider != "" {
			existing.Provider = card.Provider
		}
		if existing.Status == "" {
			existing.Status = "unknown"
		}
		return
	}
	c := card
	if c.Reputation == 0 {
		c.Reputation = InitialReputation
	}
	if c.Risk == 0 {
		c.Risk = InitialRisk
	}
	if c.Status == "" {
		c.Status = "unknown"
	}
	r.order = append(r.order, c.ID)
	r.cards[c.ID] = &c
}

// Get returns a copy of one card.
func (r *Registry) Get(id string) (AgentCard, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.cards[id]
	if !ok {
		return AgentCard{}, false
	}
	return *c, true
}

// Snapshot returns deep copies of every card, sorted by id.
func (r *Registry) Snapshot() []AgentCard {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]AgentCard, 0, len(r.cards))
	for _, id := range r.order {
		c := r.cards[id]
		cp := *c
		cp.Tags = append([]string{}, c.Tags...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// OnlineCount returns how many agents are currently routable.
func (r *Registry) OnlineCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, c := range r.cards {
		if c.Status == "online" {
			n++
		}
	}
	return n
}

// SetHealth records a health observation for one agent.
func (r *Registry) SetHealth(id, status, provider, model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cards[id]
	if !ok {
		return
	}
	c.Status = status
	if provider != "" {
		c.Provider = provider
	}
	if model != "" {
		c.Model = model
	}
}

// RecordResult applies one observed execution outcome to an agent and returns the reputation and
// risk before and after the update, so the trace can show exactly how the metrics moved.
func (r *Registry) RecordResult(id string, ok, timedOut bool, latencyMs int64, errMsg string) (repBefore, riskBefore, repAfter, riskAfter float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, found := r.cards[id]
	if !found {
		return InitialReputation, InitialRisk, InitialReputation, InitialRisk
	}
	repBefore, riskBefore = c.Reputation, c.Risk
	c.Executions++
	c.TotalLatencyMs += latencyMs
	c.AvgLatencyMs = c.TotalLatencyMs / int64(c.Executions)
	if ok {
		c.Success++
		c.Reputation = clamp01(repBefore + AlphaReputation*(1-repBefore))
		c.Risk = clamp01(riskBefore - BetaRisk*riskBefore)
		c.LastError = ""
	} else {
		c.Failure++
		if timedOut {
			c.Timeouts++
		}
		c.Reputation = clamp01(repBefore - AlphaReputation*repBefore)
		c.Risk = clamp01(riskBefore + BetaRisk*(1-riskBefore))
		if errMsg != "" {
			c.LastError = errMsg
		}
	}
	c.LastSeen = time.Now().UTC().Format(time.RFC3339)
	return repBefore, riskBefore, c.Reputation, c.Risk
}

// Save persists the mutable agent metrics so reputation and risk survive a restart.
func (r *Registry) Save() error {
	if r.stateFile == "" {
		return nil
	}
	r.mu.RLock()
	p := persisted{}
	for _, id := range r.order {
		c := r.cards[id]
		p.Cards = append(p.Cards, persistedCard{
			ID: c.ID, Reputation: c.Reputation, Risk: c.Risk, Success: c.Success, Failure: c.Failure,
			Timeouts: c.Timeouts, Executions: c.Executions, TotalLatencyMs: c.TotalLatencyMs,
			AvgLatencyMs: c.AvgLatencyMs, LastSeen: c.LastSeen, LastError: c.LastError,
		})
	}
	r.mu.RUnlock()
	if dir := filepath.Dir(r.stateFile); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.stateFile, b, 0o644)
}

// Load restores persisted metrics onto the matching registered agents.
func (r *Registry) Load() error {
	if r.stateFile == "" {
		return nil
	}
	raw, err := os.ReadFile(r.stateFile)
	if err != nil {
		return err
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, pc := range p.Cards {
		if c, ok := r.cards[pc.ID]; ok {
			c.Reputation = pc.Reputation
			c.Risk = pc.Risk
			c.Success = pc.Success
			c.Failure = pc.Failure
			c.Timeouts = pc.Timeouts
			c.Executions = pc.Executions
			c.TotalLatencyMs = pc.TotalLatencyMs
			c.AvgLatencyMs = pc.AvgLatencyMs
			c.LastSeen = pc.LastSeen
			c.LastError = pc.LastError
		}
	}
	return nil
}

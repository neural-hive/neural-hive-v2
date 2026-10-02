package hub

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/neural-hive/hive-node/internal/deepworker"
)

// Configurable algorithms and simulations (proposal 4.2): routing mode (selfish 4.2.2 /
// LinUCB 4.2.3), aggregation mode (mean / majority / Krum / Multi-Krum 4.2.5) and a byzantine
// simulation. Nothing here is a UI-only toggle: the routing mode changes agent selection, the
// aggregation mode changes the final answer and the byzantine count changes what the agents
// actually return. Every decision is recorded in the execution trace.

// AlgConfig is the per-task algorithm configuration supplied by the requester.
type AlgConfig struct {
	Routing     string `json:"routing"`
	Aggregation string `json:"aggregation"`
	Byzantine   int    `json:"byzantine"`
	// KrumM is the Multi-Krum parameter m: how many best-ranked answers to keep (0 = the
	// default n-f, i.e. tolerate f faults). Only meaningful for the multi-krum rule.
	KrumM    int  `json:"krumM"`
	Simulate bool `json:"simulate"`
	// HistoryLimit caps how many prior chat turns are forwarded to the agents for this request
	// (0 = use the server default HUB_HISTORY_LIMIT, -1 = no cap i.e. the whole conversation).
	HistoryLimit int                   `json:"historyLimit,omitempty"`
	History      []deepworker.ChatTurn `json:"history,omitempty"`
}

// AlgorithmSummary is the resolved algorithm configuration recorded on the result.
type AlgorithmSummary struct {
	Routing        string  `json:"routing"`
	Aggregation    string  `json:"aggregation"`
	Byzantine      int     `json:"byzantine"`
	Committee      int     `json:"committee"`
	FaultTol       int     `json:"faultTolerance"`
	PriceOfAnarchy float64 `json:"priceOfAnarchy"`
	RouteModeUsed  string  `json:"routeModeUsed"`
	KrumKeep       int     `json:"krumKeep"`
	KrumOutliers   int     `json:"krumOutliers"`
	AggregatorNote string  `json:"aggregatorNote"`
}

func normalizeAgg(a string) string {
	switch strings.ToLower(strings.TrimSpace(a)) {
	case "mean", "average", "avg":
		return "mean"
	case "majority", "plurality", "vote":
		return "majority"
	case "multi-krum", "multikrum", "multi_krum":
		return "multi-krum"
	case "krum":
		return "krum"
	default:
		return ""
	}
}

func normalizeRouting(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "linucb", "bandit":
		return "linucb"
	default:
		return "selfish"
	}
}

func (h *Hub) resolveAlg(cfg AlgConfig) AlgConfig {
	out := AlgConfig{Routing: normalizeRouting(cfg.Routing), Aggregation: normalizeAgg(cfg.Aggregation), Byzantine: cfg.Byzantine, KrumM: cfg.KrumM, Simulate: cfg.Simulate, HistoryLimit: cfg.HistoryLimit, History: cfg.History}
	if out.Aggregation == "" {
		out.Aggregation = "mean"
	}
	if out.Byzantine < 0 {
		out.Byzantine = 0
	}
	return out
}

// priceHive is the agent advertised price in HIVE (0 when unset). Price is a routing signal.
func (c AgentCard) priceHive() float64 { return c.PriceHive }

// selfishCost is what a task perceives an agent to cost: advertised load plus advertised price.
func selfishCost(card AgentCard) float64 {
	load := 0.0
	if card.Executions > 0 {
		load = clamp01(float64(card.TotalLatencyMs) / 100000.0)
	}
	return load + card.priceHive()
}

// priceOfAnarchy is the measured proxy of section 4.2.2: the gap between the average wait time
// under selfish routing and an estimate of the best achievable wait time, bounded in [1,inf).
func priceOfAnarchy(cards []AgentCard) float64 {
	if len(cards) == 0 {
		return 1
	}
	best := math.MaxFloat64
	sum := 0.0
	for _, c := range cards {
		w := 1.0 + float64(c.AvgLatencyMs)/1000.0
		sum += w
		if w < best {
			best = w
		}
	}
	if best <= 0 {
		return 1
	}
	poa := (sum / float64(len(cards))) / best
	if poa < 1 {
		poa = 1
	}
	return poa
}

// ---------------------------------------------------------------------------------------------
// LinUCB contextual bandit (4.2.3)
// ---------------------------------------------------------------------------------------------

type bandit struct {
	mu    sync.Mutex
	dim   int
	A     map[string][][]float64
	b     map[string][]float64
	alpha float64
}

func newBandit(dim int) *bandit {
	if dim <= 0 {
		dim = 4
	}
	return &bandit{dim: dim, A: map[string][][]float64{}, b: map[string][]float64{}, alpha: 0.35}
}

func identity(n int) [][]float64 {
	m := make([][]float64, n)
	for i := range m {
		m[i] = make([]float64, n)
		m[i][i] = 1
	}
	return m
}

func matVec(m [][]float64, v []float64) []float64 {
	out := make([]float64, len(m))
	for i := range m {
		s := 0.0
		for j := range v {
			s += m[i][j] * v[j]
		}
		out[i] = s
	}
	return out
}

// shermanMorrison applies A = A - A u u^T A / (1 + u^T A u), the cheap inverse of A + x x^T.
func shermanMorrison(a [][]float64, x []float64) [][]float64 {
	n := len(a)
	ax := matVec(a, x)
	xtax := 0.0
	for i := 0; i < n; i++ {
		xtax += x[i] * ax[i]
	}
	denom := 1.0 + xtax
	out := make([][]float64, n)
	for i := 0; i < n; i++ {
		out[i] = make([]float64, n)
		for j := 0; j < n; j++ {
			out[i][j] = a[i][j] - (ax[i]*ax[j])/denom
		}
	}
	return out
}

// contextFor builds the feature vector for a subtask: bias, tag overlap, price, inverse latency.
func contextFor(sub Subtask, card AgentCard) []float64 {
	overlap := 0.0
	if len(sub.Tags) > 0 {
		overlap = float64(len(intersect(card.Tags, sub.Tags))) / float64(len(sub.Tags))
	}
	inv := 1.0
	if card.AvgLatencyMs > 0 {
		inv = 1.0 / (1.0 + float64(card.AvgLatencyMs)/1000.0)
	}
	return []float64{1.0, overlap, clamp01(card.priceHive()), inv}
}

func (b *bandit) score(agentID string, x []float64) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.A[agentID]
	if !ok {
		a = identity(b.dim)
		b.A[agentID] = a
		b.b[agentID] = make([]float64, b.dim)
	}
	theta := matVec(a, b.b[agentID])
	mean := 0.0
	for i := range x {
		mean += theta[i] * x[i]
	}
	ax := matVec(a, x)
	bonus := 0.0
	for i := range x {
		bonus += x[i] * ax[i]
	}
	return mean + b.alpha*math.Sqrt(math.Max(bonus, 0))
}

func (b *bandit) update(agentID string, x []float64, reward float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.A[agentID]
	if !ok {
		a = identity(b.dim)
		b.A[agentID] = a
		b.b[agentID] = make([]float64, b.dim)
	}
	b.A[agentID] = shermanMorrison(a, x)
	bb := b.b[agentID]
	for i := range x {
		bb[i] += reward * x[i]
	}
}

// ---------------------------------------------------------------------------------------------
// Krum / Multi-Krum over real answers (4.2.5)
// ---------------------------------------------------------------------------------------------

// answerDistance is 1 - Jaccard token similarity between two answers: honest answers on the same
// subtask sit close together and a byzantine answer sits far away.
func answerDistance(a, b string) float64 {
	ta := tokenSet(a)
	tb := tokenSet(b)
	if len(ta) == 0 && len(tb) == 0 {
		return 0
	}
	inter := 0
	for t := range ta {
		if tb[t] {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	if union == 0 {
		return 0
	}
	return 1.0 - float64(inter)/float64(union)
}

func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 97 && r <= 122) && !(r >= 48 && r <= 57)
	}) {
		if len(t) >= 3 {
			out[t] = true
		}
	}
	return out
}

// krumRank runs the Krum rule over real answers. It returns the winning index, for Multi-Krum the
// ordered list of the (n-f) best indices, and the per-candidate score.
// krumRank runs the Krum rule over real answers. It returns the winning index, for Multi-Krum the
// ordered list of the best indices, and the per-candidate score. m is the Multi-Krum parameter
// (how many answers to keep); m <= 0 means the standard n-f (tolerate f faults). A caller-supplied
// m is clamped to [1, n-f] so it can never keep fewer than the fault-tolerance bound or more than
// the number of answers actually received.
func krumRank(good []AgentResult, f, m int) (winner int, selected []int, scores []float64, err error) {
	n := len(good)
	if n < 2*f+3 {
		return -1, nil, nil, fmt.Errorf("not enough responses for fault tolerance: have %d, need at least %d (f=%d)", n, 2*f+3, f)
	}
	keep := n - f - 2
	scores = make([]float64, n)
	for i := 0; i < n; i++ {
		d := make([]float64, 0, n-1)
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			d = append(d, answerDistance(good[i].Answer, good[j].Answer))
		}
		sort.Float64s(d)
		s := 0.0
		for k := 0; k < keep; k++ {
			s += d[k]
		}
		scores[i] = s
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] < scores[order[j]] })
	winner = order[0]
	// Multi-Krum keeps the n-f best answers by default; an explicit m (clamped to [1, n-f])
	// lets the requester trade liveness for a tighter committee while still tolerating f faults.
	maxKeep := n - f
	if maxKeep < 1 {
		maxKeep = 1
	}
	if maxKeep > n {
		maxKeep = n
	}
	multi := maxKeep
	if m > 0 {
		multi = m
		if multi < 1 {
			multi = 1
		}
		if multi > maxKeep {
			multi = maxKeep
		}
	}
	selected = order[:multi]
	return winner, selected, scores, nil
}

// maxFaults mirrors the consensus package: the largest f an n-agent committee tolerates.
func maxFaults(n int) int {
	if n < 3 {
		return 0
	}
	return (n - 3) / 2
}

// selectKrumAnswers applies the Krum / Multi-Krum rule. When every answer comes from the same
// subtask the whole batch is one committee. When answers come from several subtasks they are
// grouped by subtask and Krum runs inside each group, because answers to different questions are
// not comparable; a group that cannot meet the n >= 2f+3 bound keeps all of its answer and the
// reason is recorded in the note.
func selectKrumAnswers(good []AgentResult, f, m int, multi bool) (chosen []AgentResult, outliers int, note string, err error) {
	groups := map[string][]int{}
	order := []string{}
	for i, r := range good {
		if _, ok := groups[r.SubtaskID]; !ok {
			order = append(order, r.SubtaskID)
		}
		groups[r.SubtaskID] = append(groups[r.SubtaskID], i)
	}
	if len(order) <= 1 {
		f2 := f
		if f2 > maxFaults(len(good)) {
			f2 = maxFaults(len(good))
		}
		winner, selected, scores, kerr := krumRank(good, f2, m)
		if kerr != nil {
			return good, 0, "Krum fell back to all answers: " + kerr.Error(), kerr
		}
		if multi {
			for _, idx := range selected {
				chosen = append(chosen, good[idx])
			}
			return chosen, len(good) - len(selected), fmt.Sprintf("Multi-Krum kept %d of %d answers (m=%d, f=%d)", len(selected), len(good), len(selected), f2), nil
		}
		return []AgentResult{good[winner]}, len(good) - 1, fmt.Sprintf("Krum selected %s (score %.4f)", good[winner].AgentID, scores[winner]), nil
	}
	var parts []string
	for _, sid := range order {
		idxs := groups[sid]
		gf := make([]AgentResult, 0, len(idxs))
		for _, i := range idxs {
			gf = append(gf, good[i])
		}
		f2 := f
		if f2 > maxFaults(len(gf)) {
			f2 = maxFaults(len(gf))
		}
		if len(gf) < 2*f2+3 {
			chosen = append(chosen, gf...)
			parts = append(parts, fmt.Sprintf("%s: %d answer(s) kept (need %d for f=%d)", sid, len(gf), 2*f+3, f))
			continue
		}
		if multi {
			_, selected, _, kerr := krumRank(gf, f2, m)
			if kerr != nil {
				chosen = append(chosen, gf...)
				continue
			}
			for _, idx := range selected {
				chosen = append(chosen, gf[idx])
			}
			outliers += len(gf) - len(selected)
			parts = append(parts, fmt.Sprintf("%s: %d/%d kept", sid, len(selected), len(gf)))
		} else {
			winner, _, _, kerr := krumRank(gf, f2, m)
			if kerr != nil {
				chosen = append(chosen, gf...)
				continue
			}
			chosen = append(chosen, gf[winner])
			outliers += len(gf) - 1
			parts = append(parts, fmt.Sprintf("%s: kept %s", sid, gf[winner].AgentID))
		}
	}
	return chosen, outliers, "Per-subtask Krum (" + strings.Join(parts, "; ") + ")", nil
}

// Package hnsw implements Hierarchical Navigable Small World approximate nearest-neighbour search
// (Malkov & Yashunin, 2018), used by Neural Hive to route a task embedding to candidate agents
// in roughly O(log N) instead of scanning every registered agent. Proposal section 4.2.1.
package hnsw

import (
	"math"
	"math/rand"
	"sort"
	"sync"

	"github.com/neural-hive/hive-node/internal/capability"
)

// Result is a search hit.
type Result struct {
	ID       string
	Distance float64
}

// Node is a graph vertex holding an agent capability vector.
type Node struct {
	ID        string
	Vec       capability.Vector
	Level     int
	Neighbors [][]int
	Deleted   bool
}

// Index is a deterministic HNSW graph (insertion order and RNG seed fully determine the graph).
type Index struct {
	mu          sync.RWMutex
	M           int
	EfBuild     int
	EfSearch    int
	mL          float64
	Dim         int
	MaxLevelCap int
	Nodes       []*Node
	byID        map[string]int
	Entry       int
	TopLevel    int
	rng         *rand.Rand
	Dist        func(a, b capability.Vector) float64
}

type pair struct {
	id   int
	dist float64
}

// New creates an HNSW index. M is the per-level out-degree, efBuild/efSearch the beam widths.
func New(m, efBuild, efSearch, dim int, seed int64) *Index {
	if m <= 0 {
		m = 16
	}
	return &Index{
		M:           m,
		EfBuild:     efBuild,
		EfSearch:    efSearch,
		mL:          1.0 / math.Log(float64(m)),
		Dim:         dim,
		MaxLevelCap: 16,
		byID:        make(map[string]int),
		Entry:       -1,
		TopLevel:    -1,
		rng:         rand.New(rand.NewSource(seed)),
		Dist:        capability.Distance,
	}
}

// Len returns the number of live nodes.
func (h *Index) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.byID)
}

func (h *Index) randomLevel() int {
	lvl := int(-math.Log(h.rng.Float64()) * h.mL)
	if lvl > h.MaxLevelCap {
		lvl = h.MaxLevelCap
	}
	if lvl < 0 {
		lvl = 0
	}
	return lvl
}

func (h *Index) searchLayer(q capability.Vector, eps []pair, ef int, level int) []pair {
	visited := make(map[int]bool, ef*4)
	candidates := make([]pair, 0, ef*2)
	results := make([]pair, 0, ef)
	for _, e := range eps {
		if visited[e.id] {
			continue
		}
		visited[e.id] = true
		d := h.Dist(q, h.Nodes[e.id].Vec)
		candidates = append(candidates, pair{e.id, d})
		results = append(results, pair{e.id, d})
	}
	sortPairs(candidates)
	sortPairs(results)
	for len(candidates) > 0 {
		c := candidates[0]
		candidates = candidates[1:]
		if len(results) >= ef && c.dist > results[len(results)-1].dist {
			break
		}
		if level >= len(h.Nodes[c.id].Neighbors) {
			continue
		}
		for _, nb := range h.Nodes[c.id].Neighbors[level] {
			if visited[nb] {
				continue
			}
			visited[nb] = true
			d := h.Dist(q, h.Nodes[nb].Vec)
			if len(results) < ef || d < results[len(results)-1].dist {
				candidates = append(candidates, pair{nb, d})
				sortPairs(candidates)
				results = append(results, pair{nb, d})
				sortPairs(results)
				if len(results) > ef {
					results = results[:ef]
				}
			}
		}
	}
	return results
}

func sortPairs(p []pair) {
	sort.Slice(p, func(i, j int) bool { return p[i].dist < p[j].dist })
}

// Insert adds or replaces an agent vector.
func (h *Index) Insert(id string, vec capability.Vector) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.byID[id]; ok {
		h.Nodes[old].Vec = vec
		return
	}
	level := h.randomLevel()
	n := &Node{ID: id, Vec: vec, Level: level, Neighbors: make([][]int, level+1)}
	idx := len(h.Nodes)
	h.Nodes = append(h.Nodes, n)
	h.byID[id] = idx
	if h.Entry < 0 {
		h.Entry = idx
		h.TopLevel = level
		return
	}

	cur := h.Entry
	for l := h.TopLevel; l > level; l-- {
		cur = h.greedyDescent(vec, cur, l)
	}
	eps := []pair{{cur, h.Dist(vec, h.Nodes[cur].Vec)}}
	top := level
	if h.TopLevel < top {
		top = h.TopLevel
	}
	for l := top; l >= 0; l-- {
		found := h.searchLayer(vec, eps, h.EfBuild, l)
		sel := selectNearest(found, h.M)
		ids := make([]int, 0, len(sel))
		for _, s := range sel {
			ids = append(ids, s.id)
		}
		n.Neighbors[l] = ids
		for _, nb := range ids {
			h.Nodes[nb].Neighbors[l] = append(h.Nodes[nb].Neighbors[l], idx)
			if len(h.Nodes[nb].Neighbors[l]) > h.M {
				h.Nodes[nb].Neighbors[l] = h.pruneLocked(nb, l, h.M)
			}
		}
		if len(found) > 0 {
			eps = found
		}
	}
	if level > h.TopLevel {
		h.TopLevel = level
		h.Entry = idx
	}
}

func (h *Index) greedyDescent(q capability.Vector, cur, level int) int {
	improved := true
	for improved {
		improved = false
		if level >= len(h.Nodes[cur].Neighbors) {
			return cur
		}
		best := cur
		bestD := h.Dist(q, h.Nodes[cur].Vec)
		for _, nb := range h.Nodes[cur].Neighbors[level] {
			d := h.Dist(q, h.Nodes[nb].Vec)
			if d < bestD {
				bestD = d
				best = nb
				improved = true
			}
		}
		cur = best
	}
	return cur
}

func (h *Index) pruneLocked(node, level, m int) []int {
	nbs := h.Nodes[node].Neighbors[level]
	ps := make([]pair, 0, len(nbs))
	for _, nb := range nbs {
		ps = append(ps, pair{nb, h.Dist(h.Nodes[node].Vec, h.Nodes[nb].Vec)})
	}
	sortPairs(ps)
	seen := make(map[int]bool, len(ps))
	out := make([]int, 0, m)
	for _, p := range ps {
		if seen[p.id] {
			continue
		}
		seen[p.id] = true
		out = append(out, p.id)
		if len(out) >= m {
			break
		}
	}
	return out
}

func selectNearest(cands []pair, m int) []pair {
	cp := make([]pair, len(cands))
	copy(cp, cands)
	sortPairs(cp)
	seen := make(map[int]bool, len(cp))
	out := make([]pair, 0, m)
	for _, c := range cp {
		if seen[c.id] {
			continue
		}
		seen[c.id] = true
		out = append(out, c)
		if len(out) >= m {
			break
		}
	}
	return out
}

// Search returns the k nearest live agents to the query vector.
func (h *Index) Search(q capability.Vector, k int) []Result {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.Entry < 0 || k <= 0 {
		return nil
	}
	cur := h.Entry
	for l := h.TopLevel; l > 0; l-- {
		cur = h.greedyDescent(q, cur, l)
	}
	ef := h.EfSearch
	if ef < k {
		ef = k
	}
	found := h.searchLayer(q, []pair{{cur, h.Dist(q, h.Nodes[cur].Vec)}}, ef, 0)
	out := make([]Result, 0, k)
	for _, f := range found {
		if h.Nodes[f.id].Deleted {
			continue
		}
		out = append(out, Result{ID: h.Nodes[f.id].ID, Distance: f.dist})
		if len(out) >= k {
			break
		}
	}
	return out
}

// Delete soft-deletes an agent so it stops appearing in search results.
func (h *Index) Delete(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	idx, ok := h.byID[id]
	if !ok {
		return false
	}
	h.Nodes[idx].Deleted = true
	delete(h.byID, id)
	return true
}

// Has reports whether the agent is present and live.
func (h *Index) Has(id string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	idx, ok := h.byID[id]
	return ok && !h.Nodes[idx].Deleted
}

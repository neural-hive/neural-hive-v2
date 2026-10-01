// Package capability builds the deterministic skill embeddings that describe what an agent can do.
//
// Proposal 4.2.1: every agent publishes a small vector describing its declared skills and
// benchmark scores. We build that vector by feature-hashing the declared skill tokens into a
// fixed-dimension space and L2-normalising the result. This is deterministic, needs no model,
// and keeps the same vector reproducible on-chain (the registry stores it as int256[8]).
package capability

import (
	"math"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

// Vector is a dense capability embedding.
type Vector []float64

// Dim is the protocol-wide embedding dimension (matches CapabilityRegistry.EMBEDDING_DIM).
const Dim = 8

// Scale is the fixed-point scale used when a capability vector is stored on-chain as int256[8].
const Scale = 1e6

// FromSkills feature-hashes skills into a Dim-dimensional unit vector.
func FromSkills(skills []string) Vector {
	v := make(Vector, Dim)
	for _, s := range skills {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		h := crypto.Keccak256([]byte(s))
		idx := int(h[0]) % Dim
		sign := 1.0
		if h[1]%2 == 1 {
			sign = -1.0
		}
		weight := 1.0 + float64(h[2])/255.0
		v[idx] += sign * weight
	}
	return v.Normalized()
}

// Normalized returns the L2-normalised copy of v (a zero vector stays zero).
func (v Vector) Normalized() Vector {
	n := v.Norm()
	out := make(Vector, len(v))
	if n == 0 {
		return out
	}
	for i := range v {
		out[i] = v[i] / n
	}
	return out
}

// Norm returns the Euclidean norm of v.
func (v Vector) Norm() float64 {
	s := 0.0
	for _, x := range v {
		s += x * x
	}
	return math.Sqrt(s)
}

// Cosine returns the cosine similarity in [-1, 1].
func Cosine(a, b Vector) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	dot, na, nb := 0.0, 0.0, 0.0
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Euclidean returns the L2 distance between two vectors.
func Euclidean(a, b Vector) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	s := 0.0
	for i := 0; i < n; i++ {
		d := a[i] - b[i]
		s += d * d
	}
	return math.Sqrt(s)
}

// Distance is the metric used by the HNSW index (1 - cosine, so lower is closer).
func Distance(a, b Vector) float64 { return 1 - Cosine(a, b) }

// ToOnChain scales a unit vector to the signed fixed-point form stored in the registry.
func (v Vector) ToOnChain() []int64 {
	out := make([]int64, Dim)
	for i := 0; i < Dim && i < len(v); i++ {
		out[i] = int64(math.Round(v[i] * Scale))
	}
	return out
}

// FromOnChain converts the stored fixed-point vector back into a Vector.
func FromOnChain(vals []int64) Vector {
	out := make(Vector, len(vals))
	for i, x := range vals {
		out[i] = float64(x) / Scale
	}
	return out.Normalized()
}

// Vocabulary is a canonical, sorted list of the skill tokens used in the prototype.
func Vocabulary() []string {
	v := []string{
		"anomaly-detection", "code-review", "compliance", "defi-strategy", "explain",
		"liquidity-management", "market-data", "moderation", "risk-scoring", "sentiment",
		"smart-contract-audit", "summarization", "translation", "trading-signal",
	}
	sort.Strings(v)
	return v
}

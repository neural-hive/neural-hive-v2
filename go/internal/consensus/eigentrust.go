// EigenTrust reputation (proposal 4.2.6): trust propagates through the network the way PageRank
// ranks pages, so manufactured reputation is far more expensive than gaming a star rating.
package consensus

import "math"

// EigenTrustResult carries the converged global trust vector and diagnostics.
type EigenTrustResult struct {
	Agents     []string
	Scores     []float64
	Iterations int
	Converged  bool
}

// RowNormalize normalises each row of the local trust matrix to sum to 1. Rows with no
// outgoing trust fall back to the pre-trust distribution, which keeps the iteration well defined
// for isolated or new agents.
func RowNormalize(m [][]float64, preTrust []float64) [][]float64 {
	n := len(m)
	out := make([][]float64, n)
	for i := 0; i < n; i++ {
		out[i] = make([]float64, n)
		sum := 0.0
		for j := 0; j < n; j++ {
			if m[i][j] > 0 {
				sum += m[i][j]
			}
		}
		if sum == 0 {
			for j := 0; j < n; j++ {
				if len(preTrust) == n {
					out[i][j] = preTrust[j]
				} else {
					out[i][j] = 1.0 / float64(n)
				}
			}
			continue
		}
		for j := 0; j < n; j++ {
			if m[i][j] > 0 {
				out[i][j] = m[i][j] / sum
			}
		}
	}
	return out
}

// EigenTrust runs the damped power iteration t <- (1-a) C^T t + a p until convergence.
// With alpha = 0 this is the plain iteration from the proposal pseudocode; the damping term keeps
// the vector from collapsing into disconnected cliques and is the textbook formulation from
// Kamvar, Schlosser & Garcia-Molina (2003).
func EigenTrust(agents []string, localTrust [][]float64, preTrust []float64, alpha float64, maxIter int, eps float64) EigenTrustResult {
	n := len(agents)
	if n == 0 {
		return EigenTrustResult{}
	}
	if len(preTrust) != n {
		preTrust = make([]float64, n)
		for i := range preTrust {
			preTrust[i] = 1.0 / float64(n)
		}
	}
	c := RowNormalize(localTrust, preTrust)

	t := make([]float64, n)
	for i := range t {
		t[i] = 1.0 / float64(n)
	}
	next := make([]float64, n)
	iter := 0
	converged := false
	for iter = 0; iter < maxIter; iter++ {
		for i := 0; i < n; i++ {
			acc := 0.0
			for j := 0; j < n; j++ {
				acc += c[j][i] * t[j]
			}
			next[i] = (1-alpha)*acc + alpha*preTrust[i]
		}
		sum := 0.0
		for i := range next {
			if next[i] < 0 {
				next[i] = 0
			}
			sum += next[i]
		}
		if sum == 0 {
			sum = 1
		}
		delta := 0.0
		for i := range next {
			next[i] /= sum
			delta += math.Abs(next[i] - t[i])
		}
		copy(t, next)
		if delta < eps {
			converged = true
			iter++
			break
		}
	}
	return EigenTrustResult{Agents: agents, Scores: t, Iterations: iter, Converged: converged}
}

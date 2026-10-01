// Package consensus holds the off-chain reference implementations of the aggregation, reputation
// and dispute algorithms: Krum (4.2.5), EigenTrust (4.2.6) and Hive Snowball (4.2.8).
//
// These mirror the on-chain rules exactly so that the coordinator, agents and tests can
// independently recompute what the contracts do, and so a relayer cannot smuggle a different
// answer past the protocol.
package consensus

import (
	"errors"
	"math"
	"sort"
)

// ErrNotEnoughResponses is returned when a committee cannot tolerate f faults (needs n >= 2f+3).
var ErrNotEnoughResponses = errors.New("not enough responses for the requested fault tolerance")

// Krum runs the Krum rule over scalar outputs and returns the index of the selected output.
// It is the exact procedure from the proposal: for each i, sort the distances to every other
// output, sum the (n - f - 2) smallest, and return the argmin.
func Krum(values []float64, f int) (int, error) {
	n := len(values)
	if n < 2*f+3 {
		return 0, ErrNotEnoughResponses
	}
	keep := n - f - 2
	scores := make([]float64, n)
	for i := 0; i < n; i++ {
		d := make([]float64, 0, n-1)
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			d = append(d, math.Abs(values[i]-values[j]))
		}
		sort.Float64s(d)
		s := 0.0
		for k := 0; k < keep; k++ {
			s += d[k]
		}
		scores[i] = s
	}
	best := 0
	for i := 1; i < n; i++ {
		if scores[i] < scores[best] {
			best = i
		}
	}
	return best, nil
}

// KrumScores exposes the per-candidate Krum scores for auditing.
func KrumScores(values []float64, f int) ([]float64, error) {
	n := len(values)
	if n < 2*f+3 {
		return nil, ErrNotEnoughResponses
	}
	keep := n - f - 2
	scores := make([]float64, n)
	for i := 0; i < n; i++ {
		d := make([]float64, 0, n-1)
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			d = append(d, math.Abs(values[i]-values[j]))
		}
		sort.Float64s(d)
		s := 0.0
		for k := 0; k < keep; k++ {
			s += d[k]
		}
		scores[i] = s
	}
	return scores, nil
}

// PluralityHash returns the most common hash and the index of the first agent that reported it.
func PluralityHash(hashes []string) (int, string) {
	best, bestCount := 0, -1
	for i := range hashes {
		c := 0
		for j := range hashes {
			if hashes[i] == hashes[j] {
				c++
			}
		}
		if c > bestCount {
			bestCount = c
			best = i
		}
	}
	if bestCount < 0 {
		return -1, ""
	}
	return best, hashes[best]
}

// MaxFaults is the largest f a committee of size n can tolerate under n >= 2f + 3.
func MaxFaults(n int) int {
	if n < 3 {
		return 0
	}
	return (n - 3) / 2
}

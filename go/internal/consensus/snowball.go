// Hive Snowball (proposal 4.2.8): repeated random sampling consensus, the same metastable idea
// Avalanche itself runs on, applied one layer up to settle verifier disagreement.
package consensus

import "math/rand"

// SnowballRound records one polling round of the snowball.
type SnowballRound struct {
	Round      int
	Sample     []string
	Majority   string
	Preference string
	Confidence int
}

// Snowball polls random samples of verifier opinions until one preference survives beta rounds in
// a row, or the round limit is reached. It returns the final preference, its confidence and the
// per-round trace, so the outcome can be audited.
func Snowball(preference string, opinions []string, beta, k int, rng *rand.Rand, maxRounds int) (string, int, []SnowballRound) {
	confidence := 0
	trace := make([]SnowballRound, 0, maxRounds)
	if len(opinions) == 0 {
		return preference, confidence, trace
	}
	if k > len(opinions) {
		k = len(opinions)
	}
	for round := 1; round <= maxRounds; round++ {
		idx := rng.Perm(len(opinions))[:k]
		sample := make([]string, 0, k)
		for _, i := range idx {
			sample = append(sample, opinions[i])
		}
		majority := majorityOf(sample)
		if majority == preference {
			confidence++
		} else {
			preference = majority
			confidence = 1
		}
		trace = append(trace, SnowballRound{Round: round, Sample: sample, Majority: majority, Preference: preference, Confidence: confidence})
		if confidence >= beta {
			break
		}
	}
	return preference, confidence, trace
}

func majorityOf(sample []string) string {
	best, bestCount := "", -1
	for i := range sample {
		c := 0
		for j := range sample {
			if sample[i] == sample[j] {
				c++
			}
		}
		if c > bestCount {
			bestCount = c
			best = sample[i]
		}
	}
	return best
}

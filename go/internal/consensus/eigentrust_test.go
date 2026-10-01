package consensus

import (
	"math"
	"testing"
)

func TestRowNormalizeHandlesEmptyRows(t *testing.T) {
	m := [][]float64{{1, 2}, {0, 0}}
	pre := []float64{0.25, 0.75}
	c := RowNormalize(m, pre)
	if math.Abs(c[0][0]-(1.0/3.0)) > 1e-9 || math.Abs(c[0][1]-(2.0/3.0)) > 1e-9 {
		t.Fatalf("row 0 must be normalised, got %v", c[0])
	}
	if c[1][0] != 0.25 || c[1][1] != 0.75 {
		t.Fatalf("a row with no outgoing trust must fall back to the pre-trust vector, got %v", c[1])
	}
}

func TestEigenTrustPropagatesTrust(t *testing.T) {
	agents := []string{"a", "b", "c"}
	m := [][]float64{
		{0, 1, 0},
		{0, 0, 1},
		{0, 0, 0},
	}
	pre := []float64{1.0 / 3, 1.0 / 3, 1.0 / 3}
	res := EigenTrust(agents, m, pre, 0.15, 500, 1e-12)
	if !res.Converged {
		t.Fatalf("the power iteration should converge")
	}
	sum := 0.0
	for _, s := range res.Scores {
		if s < 0 {
			t.Fatalf("scores must be non-negative")
		}
		sum += s
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Fatalf("scores must form a distribution, sum was %f", sum)
	}
	if !(res.Scores[2] > res.Scores[0]) {
		t.Fatalf("an agent trusted by others must outrank one that only gives trust: %v", res.Scores)
	}
}

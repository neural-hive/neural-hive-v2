package consensus

import (
	"testing"
)

func TestKrumIgnoresTheOutlier(t *testing.T) {
	values := []float64{10.0, 10.1, 9.9, 10.2, 1000.0}
	idx, err := Krum(values, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx == 4 {
		t.Fatalf("krum must never select the far outlier")
	}
}

func TestKrumRequiresEnoughResponses(t *testing.T) {
	if _, err := Krum([]float64{1, 2}, 1); err != ErrNotEnoughResponses {
		t.Fatalf("expected ErrNotEnoughResponses, got %v", err)
	}
	if MaxFaults(2) != 0 || MaxFaults(5) != 1 || MaxFaults(8) != 2 {
		t.Fatalf("MaxFaults must follow n >= 2f+3: %d %d %d", MaxFaults(2), MaxFaults(5), MaxFaults(8))
	}
}

func TestKrumScoresRankTheOutlierWorst(t *testing.T) {
	scores, err := KrumScores([]float64{1, 1, 1, 1, 9}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 5 {
		t.Fatalf("expected one score per response, got %d", len(scores))
	}
	if scores[4] <= scores[0] {
		t.Fatalf("the outlier must carry the worst krum score: %f vs %f", scores[4], scores[0])
	}
}

func TestPluralityHashPicksTheMostCommon(t *testing.T) {
	idx, h := PluralityHash([]string{"a", "a", "b"})
	if idx != 0 || h != "a" {
		t.Fatalf("expected the most common hash, got %d %s", idx, h)
	}
}

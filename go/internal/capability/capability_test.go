package capability

import (
	"math"
	"testing"
)

func TestFromSkillsIsDeterministicAndNormalised(t *testing.T) {
	a := FromSkills([]string{"risk-scoring", "explain"})
	b := FromSkills([]string{"risk-scoring", "explain"})
	if len(a) != Dim {
		t.Fatalf("expected dimension %d, got %d", Dim, len(a))
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-12 {
			t.Fatalf("FromSkills must be deterministic at index %d", i)
		}
	}
	if n := a.Norm(); math.Abs(n-1) > 1e-9 {
		t.Fatalf("expected the unit vector, norm was %f", n)
	}
	if got := FromSkills(nil); got.Norm() != 0 {
		t.Fatalf("an empty skill list must produce the zero vector")
	}
}

func TestCosineDistanceAndOnChainRoundTrip(t *testing.T) {
	a := FromSkills([]string{"moderation"})
	if c := Cosine(a, a); math.Abs(c-1) > 1e-9 {
		t.Fatalf("cosine with itself must be 1, got %f", c)
	}
	if d := Distance(a, a); math.Abs(d) > 1e-9 {
		t.Fatalf("distance to itself must be 0, got %f", d)
	}
	back := FromOnChain(a.ToOnChain())
	if c := Cosine(a, back); c < 0.999 {
		t.Fatalf("the on-chain fixed point round trip lost precision: %f", c)
	}
}

func TestDifferentSkillsProduceDifferentVectors(t *testing.T) {
	x := FromSkills([]string{"market-data"})
	y := FromSkills([]string{"code-review"})
	c := Cosine(x, y)
	if c < -1.0001 || c > 1.0001 {
		t.Fatalf("cosine must stay within [-1,1], got %f", c)
	}
	if x.Norm() == 0 || y.Norm() == 0 {
		t.Fatalf("a non-empty skill must yield a non-zero vector")
	}
	v := Vocabulary()
	if len(v) < 5 {
		t.Fatalf("expected a non-trivial skill vocabulary, got %d entries", len(v))
	}
}

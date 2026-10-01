package consensus

import (
	"math/rand"
	"testing"
)

func TestSnowballConvergesOnTheMajority(t *testing.T) {
	opinions := []string{"honest", "honest", "honest", "honest", "evil"}
	rng := rand.New(rand.NewSource(1))
	pref, conf, trace := Snowball("evil", opinions, 3, 3, rng, 50)
	if pref != "honest" {
		t.Fatalf("snowball must settle on the majority preference, got %s", pref)
	}
	if conf < 3 {
		t.Fatalf("confidence must reach beta, got %d", conf)
	}
	if len(trace) == 0 || len(trace) > 50 {
		t.Fatalf("expected a bounded round trace, got %d", len(trace))
	}
	for i := 1; i < len(trace); i++ {
		if trace[i].Round != trace[i-1].Round+1 {
			t.Fatalf("rounds must be consecutive")
		}
	}
}

func TestSnowballWithNoOpinionsIsANoOp(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	pref, conf, trace := Snowball("seed", nil, 3, 3, rng, 10)
	if pref != "seed" || conf != 0 || len(trace) != 0 {
		t.Fatalf("no verifier opinions must leave the preference untouched")
	}
}

func TestSnowballHonoursTheRoundLimit(t *testing.T) {
	opinions := []string{"a", "b", "a", "b"}
	rng := rand.New(rand.NewSource(3))
	_, conf, trace := Snowball("a", opinions, 100, 2, rng, 7)
	if len(trace) != 7 {
		t.Fatalf("the round limit must cap the trace, got %d", len(trace))
	}
	if conf >= 100 {
		t.Fatalf("an unreachable beta must never be reported as reached")
	}
}

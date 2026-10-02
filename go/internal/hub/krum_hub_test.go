package hub

import (
	"testing"
)

// res builds an AgentResult with an answer (the only field Krum uses for distance).
func resAnswer(id, ans string) AgentResult {
	return AgentResult{AgentID: id, Status: "ok", Answer: ans, SubtaskID: "s1"}
}

// TestKrumFiltersByzantineAnswer proves Krum is a real distance-based rule over real answers:
// five honest answers about distributed consensus plus one off-topic byzantine answer, Krum must
// never select the byzantine one and must only pick an honest answer.
func TestKrumFiltersByzantineAnswer(t *testing.T) {
	honest := "Distributed consensus is agreement among nodes on a shared value despite faults and delays in the network"
	good := []AgentResult{
		resAnswer("a", honest+" ."),
		resAnswer("b", honest+" !"),
		resAnswer("c", honest+" ."),
		resAnswer("d", honest+" ."),
		resAnswer("e", honest+" ."),
		resAnswer("liar", "The best pizza in town is pineapple and anchovy on a thin crust, order it now."),
	}
	winner, _, _, err := krumRank(good, 1, 0)
	if err != nil {
		t.Fatalf("krum should tolerate f=1 with n=%d: %v", len(good), err)
	}
	if good[winner].AgentID == "liar" {
		t.Fatalf("Krum selected the byzantine answer; it is a no-op")
	}
}

// TestMultiKrumKeepsM verifies the Multi-Krum m parameter is honoured: with m=3 the rule keeps
// the three best-ranked answers, and a caller-supplied m is clamped to [1, n-f].
func TestMultiKrumKeepsM(t *testing.T) {
	good := []AgentResult{
		resAnswer("a", "alpha beta gamma delta epsilon"),
		resAnswer("b", "alpha beta gamma delta epsilon"),
		resAnswer("c", "alpha beta gamma delta epsilon"),
		resAnswer("d", "alpha beta gamma delta epsilon"),
		resAnswer("e", "alpha beta gamma delta epsilon"),
		resAnswer("liar", "zzz qqq xxx unrelated nonsense"),
	}
	_, sel, _, err := krumRank(good, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel) != 3 {
		t.Fatalf("Multi-Krum kept %d answers, want m=3", len(sel))
	}
	for _, i := range sel {
		if good[i].AgentID == "liar" {
			t.Fatalf("Multi-Krum kept the byzantine answer")
		}
	}
	// An over-large m is clamped to n-f = 5.
	_, sel2, _, _ := krumRank(good, 1, 99)
	if len(sel2) != 5 {
		t.Fatalf("m=99 should clamp to n-f=5, got %d", len(sel2))
	}
}

// TestKrumRequiresN2FPlus3 checks the n >= 2f+3 fault-tolerance guard.
func TestKrumRequiresN2FPlus3(t *testing.T) {
	small := []AgentResult{resAnswer("a", "x y z"), resAnswer("b", "x y z"), resAnswer("c", "x y z")}
	if _, _, _, err := krumRank(small, 1, 0); err == nil {
		t.Fatalf("n=3 with f=1 must be rejected (needs 2f+3=5)")
	}
	seven := make([]AgentResult, 7)
	for i := range seven {
		seven[i] = resAnswer(string(rune(97+i)), "consensus agreement nodes value")
	}
	if _, _, _, err := krumRank(seven, 2, 0); err != nil {
		t.Fatalf("n=7 with f=2 must be accepted: %v", err)
	}
}

// TestMultiKrumPerSubtaskGroups checks that when answers come from several subtasks Krum runs inside
// each subtask group and never compares answers to different questions: group s1 has a liar that
// must be dropped, while group s2 has too few answers and is kept untouched.
func TestMultiKrumPerSubtaskGroups(t *testing.T) {
	good := []AgentResult{
		{AgentID: "s1a", Status: "ok", SubtaskID: "s1", Answer: "agree on a shared value among nodes"},
		{AgentID: "s1b", Status: "ok", SubtaskID: "s1", Answer: "agree on a shared value among nodes"},
		{AgentID: "s1c", Status: "ok", SubtaskID: "s1", Answer: "agree on a shared value among nodes"},
		{AgentID: "s1d", Status: "ok", SubtaskID: "s1", Answer: "agree on a shared value among nodes"},
		{AgentID: "s1e", Status: "ok", SubtaskID: "s1", Answer: "agree on a shared value among nodes"},
		{AgentID: "s1liar", Status: "ok", SubtaskID: "s1", Answer: "buy pineapples and anchovies now"},
		{AgentID: "s2a", Status: "ok", SubtaskID: "s2", Answer: "profile the request latency"},
	}
	chosen, outliers, note, err := selectKrumAnswers(good, 1, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chosen {
		if c.AgentID == "s1liar" {
			t.Fatalf("per-subtask Krum kept the liar")
		}
	}
	if outliers == 0 {
		t.Fatalf("expected at least one outlier to be dropped")
	}
	if note == "" {
		t.Fatalf("expected a note describing the per-subtask decision")
	}
}

package hnsw

import (
	"testing"

	"github.com/neural-hive/hive-node/internal/capability"
)

func skillID(i int) string { return "agent-" + string(rune(int('a')+i)) }

func buildIndex(t *testing.T) *Index {
	t.Helper()
	idx := New(8, 32, 32, capability.Dim, 42)
	skills := [][]string{
		{"risk-scoring"},
		{"explain", "summarization"},
		{"moderation"},
		{"market-data"},
		{"code-review"},
		{"sentiment"},
	}
	for i, s := range skills {
		idx.Insert(skillID(i), capability.FromSkills(s))
	}
	return idx
}

func TestInsertSearchAndLen(t *testing.T) {
	idx := buildIndex(t)
	if idx.Len() != 6 {
		t.Fatalf("expected 6 live nodes, got %d", idx.Len())
	}
	hits := idx.Search(capability.FromSkills([]string{"risk-scoring"}), 3)
	if len(hits) == 0 || len(hits) > 3 {
		t.Fatalf("search must return between 1 and 3 hits, got %d", len(hits))
	}
	for i := 1; i < len(hits); i++ {
		if hits[i-1].Distance > hits[i].Distance {
			t.Fatalf("hits must be ordered by ascending distance")
		}
	}
	if hits[0].ID != skillID(0) {
		t.Fatalf("the nearest neighbour of risk-scoring should be the risk agent, got %s", hits[0].ID)
	}
}

func TestInsertReplacesExisting(t *testing.T) {
	idx := buildIndex(t)
	idx.Insert(skillID(0), capability.FromSkills([]string{"moderation"}))
	if idx.Len() != 6 {
		t.Fatalf("re-inserting an existing id must not grow the index")
	}
}

func TestDeleteAndHas(t *testing.T) {
	idx := buildIndex(t)
	if !idx.Has(skillID(0)) {
		t.Fatalf("the freshly inserted node must be present")
	}
	if !idx.Delete(skillID(0)) {
		t.Fatalf("deleting a present node must report true")
	}
	if idx.Has(skillID(0)) {
		t.Fatalf("a deleted node must no longer be reported as live")
	}
	if idx.Len() != 5 {
		t.Fatalf("the index should hold 5 nodes after the delete, got %d", idx.Len())
	}
	if n := len(idx.Search(capability.FromSkills([]string{"risk-scoring"}), 16)); n != 5 {
		t.Fatalf("searching everything must skip the deleted node, got %d", n)
	}
}

func TestSearchEmptyIndexReturnsNil(t *testing.T) {
	idx := New(8, 32, 32, capability.Dim, 7)
	if got := idx.Search(capability.FromSkills([]string{"x"}), 5); got != nil {
		t.Fatalf("searching an empty index must return nil")
	}
}

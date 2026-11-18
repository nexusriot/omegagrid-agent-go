package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestRetrievalEval ranks every query in testdata/retrieval_eval.json
// against the real built-in catalogue and requires one of the expected tools
// within the default top-K. Ranking is lexical (no embeddings backend in
// CI), the floor a run falls back to: if this passes, a working embeddings
// model can only help. Add a case whenever a query picks the wrong tools.
func TestRetrievalEval(t *testing.T) {
	raw, err := os.ReadFile("testdata/retrieval_eval.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Q    string   `json:"q"`
		Want []string `json:"want"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	const topK = 12 // AGENT_TOOL_TOP_K default
	svc := &Service{Skills: newTestSkills(t), ToolTopK: topK, PinnedTools: []string{}}
	list, err := svc.Skills.List()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range list {
		names[s.Name] = true
	}
	hits, sumRank := 0, 0
	for _, c := range cases {
		for _, w := range c.Want {
			if !names[w] {
				t.Fatalf("%q expects unknown skill %s", c.Q, w)
			}
		}
		r, err := svc.RankTools(context.Background(), c.Q, 0)
		if err != nil {
			t.Fatal(err)
		}
		rank := -1
		for i, rt := range r.Ranked {
			for _, w := range c.Want {
				if rt.Name == w && rank < 0 {
					rank = i
				}
			}
		}
		if rank >= 0 && rank < topK {
			hits++
			sumRank += rank + 1
			continue
		}
		var top []string
		for i := 0; i < 5 && i < len(r.Ranked); i++ {
			top = append(top, r.Ranked[i].Name)
		}
		t.Errorf("%q: want %v in top %d, got rank %d (top: %s)", c.Q, c.Want, topK, rank+1, strings.Join(top, ", "))
	}
	if hits > 0 {
		t.Logf("recall@%d = %d/%d, mean rank of hits %.2f", topK, hits, len(cases), float64(sumRank)/float64(hits))
	}
}

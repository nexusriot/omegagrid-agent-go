package memory

import (
	"fmt"
	"testing"
	"time"
)

func TestReplaceMemoryIDRemovesOldDedupEntry(t *testing.T) {
	v := openTestVectorStore(t)
	for _, item := range []struct{ text, id string }{
		{"original text", "same-id"},
		{"replacement content", "same-id"},
		{"original text", "new-id"},
	} {
		result, err := v.addText(item.text, nil, item.id)
		if err != nil {
			t.Fatal(err)
		}
		if result.Skipped {
			t.Fatalf("adding %q incorrectly skipped: %+v", item.text, result)
		}
		got, err := v.getByID(item.id)
		if err != nil || got == nil || got.Text != item.text {
			t.Fatalf("stored memory = %+v, %v", got, err)
		}
	}
}

func TestExpiredMemoryDoesNotBlockReAdd(t *testing.T) {
	for _, sameText := range []bool{true, false} {
		t.Run(fmt.Sprint(sameText), func(t *testing.T) {
			v := openTestVectorStore(t)
			emb := probeVector(embDim)
			past := time.Now().Add(-time.Hour).Unix()
			if _, err := v.add("expired", map[string]any{"expires_at": past}, "old", emb); err != nil {
				t.Fatal(err)
			}
			text := "expired"
			if !sameText {
				text = "semantically identical"
			}
			res, err := v.add(text, nil, "new", emb)
			if err != nil || res.Skipped {
				t.Fatalf("expired memory blocked add: %+v, %v", res, err)
			}
			if _, err := v.purgeExpired(time.Now()); err != nil {
				t.Fatal(err)
			}
			res, err = v.add(text, nil, "another", emb)
			if err != nil || !res.Skipped || res.MemoryID != "new" {
				t.Fatalf("live dedup after purge = %+v, %v", res, err)
			}
		})
	}
}

func TestSearchFindsLiveMemoriesBehindExpiredResults(t *testing.T) {
	for _, namespaces := range [][]string{nil, {"scope"}} {
		t.Run(fmt.Sprint(namespaces), func(t *testing.T) {
			v := openTestVectorStore(t)
			v.dedupDist = -1 // retain deliberately similar fixtures
			emb, err := v.embedClient.embed("query")
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 4; i++ {
				_, err := v.add(fmt.Sprintf("expired %d", i), map[string]any{
					"namespace": "scope", "expires_at": time.Now().Add(-time.Hour).Unix(),
				}, fmt.Sprint(i), emb)
				if err != nil {
					t.Fatal(err)
				}
			}
			// The live result is less similar than all four expired results.
			for i := range emb {
				emb[i] = -emb[i]
			}
			if _, err := v.add("live", map[string]any{"namespace": "scope"}, "live", emb); err != nil {
				t.Fatal(err)
			}
			res, err := v.searchIn("query", 1, namespaces)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Hits) != 1 || res.Hits[0].ID != "live" {
				t.Fatalf("search lost the live result: %+v", res)
			}
		})
	}
}

package httpapi

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// embedServer is an Ollama /api/embed stand-in returning a hashed
// bag-of-words vector, so similar texts land close together.
func embedServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input any `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		text, _ := req.Input.(string)
		if arr, ok := req.Input.([]any); ok && len(arr) > 0 {
			text, _ = arr[0].(string)
		}
		vec := make([]float32, 16)
		for _, word := range strings.Fields(strings.ToLower(text)) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(word))
			vec[h.Sum32()%16] += 1
		}
		vec[15] += 0.01
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{vec}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func memoryDeps(t *testing.T) Deps {
	t.Helper()
	cfg := newTestDeps(t).Cfg
	cfg.OllamaURL = embedServer(t)
	cfg.OllamaTimeoutSec = 5
	cfg.DedupDistance = 0.0001
	return newTestDepsWith(t, cfg)
}

func TestMemoryNamespaceAndUpdateRoutes(t *testing.T) {
	h := NewRouter(memoryDeps(t))

	code, body := doJSON(t, h, "POST", "/api/memory/add", `{"text":"alice likes green tea","namespace":"user:alice"}`)
	if code != http.StatusOK || body["skipped"] == true {
		t.Fatalf("add = %d %v", code, body)
	}
	aliceID := body["memory_id"].(string)
	doJSON(t, h, "POST", "/api/memory/add", `{"text":"bob likes black coffee","namespace":"user:bob"}`)
	doJSON(t, h, "POST", "/api/memory/add", `{"text":"the office opens at nine"}`)

	_, body = doJSON(t, h, "POST", "/api/memory/search", `{"query":"likes","k":10,"namespaces":["user:alice",""]}`)
	for _, hit := range body["hits"].([]any) {
		if strings.Contains(hit.(map[string]any)["text"].(string), "bob") {
			t.Fatalf("alice's search returned bob's memory: %v", body["hits"])
		}
	}

	_, body = doJSON(t, h, "GET", "/api/memory?namespace=user:bob", "")
	if body["total"] != float64(1) {
		t.Fatalf("namespace list = %v", body)
	}
	_, body = doJSON(t, h, "GET", "/api/memory/namespaces", "")
	if n := len(body["namespaces"].([]any)); n != 3 {
		t.Fatalf("namespaces = %v", body)
	}

	code, body = doJSON(t, h, "PUT", "/api/memory/"+aliceID, `{"text":"alice prefers oolong","meta":{"source":"edit"},"ttl_seconds":3600}`)
	if code != http.StatusOK {
		t.Fatalf("update = %d %v", code, body)
	}
	hit := body["hit"].(map[string]any)
	meta := hit["metadata"].(map[string]any)
	if hit["text"] != "alice prefers oolong" || meta["source"] != "edit" || meta["namespace"] != "user:alice" || meta["expires_at"] == nil {
		t.Fatalf("updated hit = %v", hit)
	}
	if code, _ := doJSON(t, h, "PUT", "/api/memory/missing", `{"text":"x"}`); code != http.StatusNotFound {
		t.Fatalf("update of a missing id = %d", code)
	}
}

func TestMemoryExportImportRoutes(t *testing.T) {
	src := NewRouter(memoryDeps(t))
	doJSON(t, src, "POST", "/api/memory/add", `{"text":"first exported fact"}`)
	doJSON(t, src, "POST", "/api/memory/add", `{"text":"second exported fact","namespace":"team"}`)

	r := httptest.NewRequest("GET", "/api/memory/export?embeddings=1", nil)
	w := httptest.NewRecorder()
	src.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("export = %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"embedding"`) {
		t.Fatalf("export body = %s", w.Body.String())
	}

	// A body above the global 4 MiB cap must still reach the import handler.
	padding := strings.Repeat(" ", maxRequestBodyBytes)
	dst := NewRouter(memoryDeps(t))
	r = httptest.NewRequest("POST", "/api/memory/import", bytes.NewReader([]byte(padding+w.Body.String())))
	w2 := httptest.NewRecorder()
	dst.ServeHTTP(w2, r)
	if w2.Code != http.StatusOK {
		t.Fatalf("import = %d %s", w2.Code, w2.Body.String())
	}
	var out struct {
		Report map[string]any `json:"report"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &out)
	if out.Report["imported"] != float64(2) {
		t.Fatalf("report = %v", out.Report)
	}
	// A JSON array body works too, and a re-import reports existing records.
	var arr []any
	for _, l := range lines {
		var m any
		_ = json.Unmarshal([]byte(l), &m)
		arr = append(arr, m)
	}
	b, _ := json.Marshal(arr)
	code, body := doJSON(t, dst, "POST", "/api/memory/import", string(b))
	if code != http.StatusOK || body["report"].(map[string]any)["existing"] != float64(2) {
		t.Fatalf("array re-import = %d %v", code, body)
	}
	if code, _ := doJSON(t, dst, "POST", "/api/memory/import", "{not json"); code != http.StatusBadRequest {
		t.Fatalf("malformed import = %d", code)
	}
}

// A run in a namespace stores its vector_add there and recalls only its own
// namespace plus the shared one.
func TestQueryNamespaceScopesAgentMemory(t *testing.T) {
	d := memoryDeps(t)
	chat := &scriptedChat{replies: []string{
		`{"type":"tool_call","tool":"vector_add","args":{"text":"carol parks in bay 12","ttl_days":30}}`,
		`{"type":"final","answer":"noted"}`,
		`{"type":"tool_call","tool":"vector_search","args":{"query":"parks bay"}}`,
		`{"type":"final","answer":"searched"}`,
	}}
	h := NewRouter(withAgent(d, chat))
	if code, body := doJSON(t, h, "POST", "/api/query", `{"query":"remember","namespace":"user:carol"}`); code != http.StatusOK {
		t.Fatalf("query = %d %v", code, body)
	}
	_, body := doJSON(t, h, "GET", "/api/memory?namespace=user:carol", "")
	hits, _ := body["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("carol's namespace = %v", body)
	}
	if meta := hits[0].(map[string]any)["metadata"].(map[string]any); meta["expires_at"] == nil {
		t.Fatalf("ttl_days not applied: %v", meta)
	}

	// Another namespace's run must not find carol's memory.
	doJSON(t, h, "POST", "/api/query", `{"query":"where does carol park?","namespace":"user:dave"}`)
	turns := chat.turns()
	last := turns[len(turns)-1]
	for _, m := range last {
		if m.Role == "tool" && strings.Contains(m.Content, "bay 12") {
			t.Fatalf("dave's vector_search returned carol's memory: %s", m.Content)
		}
	}
}

// Command mockllm is a deterministic stand-in for Ollama, used by the e2e
// suite so the gateway can be exercised end-to-end with no model, no GPU and
// no network egress.
//
// It speaks the two Ollama endpoints the gateway actually calls:
//
//	POST /api/chat        → {"message":{"role":"assistant","content":"<reply>"}}
//	POST /api/embed       → {"embeddings":[[...]]}
//	POST /v1/embeddings   → {"data":[{"embedding":[...]}]}     (fallback order
//	POST /api/embeddings  → {"embedding":[...]}                 in memory/embeddings.go)
//
// plus a control API the tests drive it through:
//
//	POST /__control/script   {"replies":["<raw model text>", ...]}  — set the reply
//	                         queue and clear recorded traffic
//	GET  /__control/requests → every chat request seen since the last script,
//	                         so a test can assert what the agent actually sent
//	POST /__control/reset    — clear queue and history
//	GET  /__control/health   → {"ok":true}
//
// Chat replies are served from the queue in order; once it is drained a
// harmless "final" envelope is returned, so a runaway agent loop terminates
// instead of hanging the suite.
//
// Embeddings are a hashed bag of words: deterministic (the same text always
// embeds to the same vector, so the vector store's dedup behaves predictably)
// and similarity-bearing (texts sharing words land close together, so
// vector_search returns something meaningful to assert on).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"unicode"
)

// embedDims is the vector width. Small keeps the JSON tiny; the value only has
// to stay constant for the lifetime of a collection.
const embedDims = 64

// defaultReply is served when the script queue is empty. It ends the agent
// loop rather than letting it spin through max_steps.
const defaultReply = `{"type":"final","answer":"mockllm: no scripted reply left"}`

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type server struct {
	mu      sync.Mutex
	replies []string
	served  int
	chats   []chatRequest
	embeds  []string
}

func main() {
	addr := flag.String("addr", envOr("MOCKLLM_ADDR", ":11434"), "listen address")
	flag.Parse()

	s := &server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/embed", s.handleEmbed)
	mux.HandleFunc("/v1/embeddings", s.handleEmbedOpenAI)
	mux.HandleFunc("/api/embeddings", s.handleEmbedLegacy)
	mux.HandleFunc("/__control/script", s.handleScript)
	mux.HandleFunc("/__control/requests", s.handleRequests)
	mux.HandleFunc("/__control/reset", s.handleReset)
	mux.HandleFunc("/__control/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	log.Printf("mockllm listening on %s", *addr)
	srv := &http.Server{Addr: *addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("mockllm: %v", err)
	}
}

func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.chats = append(s.chats, req)
	reply := defaultReply
	if s.served < len(s.replies) {
		reply = s.replies[s.served]
		s.served++
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"model":   req.Model,
		"message": map[string]any{"role": "assistant", "content": reply},
		"done":    true,
	})
}

type embedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
	// The gateway sends both "input" and "prompt" so one body fits all three
	// Ollama endpoint spellings.
	Prompt string `json:"prompt"`
}

// text picks whichever field carried the payload.
func (e embedRequest) text() string {
	switch v := e.Input.(type) {
	case string:
		if v != "" {
			return v
		}
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok && s != "" {
				return s
			}
		}
	}
	return e.Prompt
}

func (s *server) readEmbedRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req embedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	text := req.text()
	s.mu.Lock()
	s.embeds = append(s.embeds, text)
	s.mu.Unlock()
	return text, true
}

func (s *server) handleEmbed(w http.ResponseWriter, r *http.Request) {
	text, ok := s.readEmbedRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"embeddings": [][]float32{embed(text)}})
}

func (s *server) handleEmbedOpenAI(w http.ResponseWriter, r *http.Request) {
	text, ok := s.readEmbedRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": []map[string]any{{"embedding": embed(text)}},
	})
}

func (s *server) handleEmbedLegacy(w http.ResponseWriter, r *http.Request) {
	text, ok := s.readEmbedRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"embedding": embed(text)})
}

func (s *server) handleScript(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Replies []string `json:"replies"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.replies = body.Replies
	s.served = 0
	s.chats = nil
	s.embeds = nil
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": len(body.Replies)})
}

func (s *server) handleRequests(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	chats := append([]chatRequest(nil), s.chats...)
	embeds := append([]string(nil), s.embeds...)
	served := s.served
	remaining := len(s.replies) - s.served
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"chats":     chats,
		"embeds":    embeds,
		"served":    served,
		"remaining": remaining,
	})
}

func (s *server) handleReset(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.replies = nil
	s.served = 0
	s.chats = nil
	s.embeds = nil
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// embed maps text to a deterministic unit vector by hashing each word into a
// bucket. Shared vocabulary ⇒ small cosine distance, which is enough for the
// suite to assert that a search finds the memory it just stored.
func embed(text string) []float32 {
	v := make([]float32, embedDims)
	for _, tok := range tokenize(text) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		v[h.Sum32()%embedDims]++
	}
	var norm float64
	for _, f := range v {
		norm += float64(f) * float64(f)
	}
	if norm == 0 {
		// Empty or punctuation-only input: chromem rejects a zero vector, so
		// give it a fixed unit vector instead.
		v[0] = 1
		return v
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		fmt.Fprintln(os.Stderr, "mockllm: encode:", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Auto-memory extraction: after a successful final answer, a detached
// goroutine asks the LLM to distil the turn into a handful of durable facts
// and stores them via vector_add's backing store.  The user-facing request
// has already returned by then, so the extra LLM round-trip costs nothing
// in perceived latency.  The vector store's SHA256 + cosine deduplication
// makes the pass idempotent: re-extracting the same conversation is a no-op.
package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
)

// extractionPromptTemplate must yield a JSON *object* (not a bare array):
// the OpenAI client requests response_format=json_object, which rejects
// top-level arrays.
const extractionPromptTemplate = `You extract durable facts from one conversation turn.

Below is a user query and the assistant's final answer. Extract at most %d facts worth remembering in future conversations.

Rules:
- Each fact must be self-contained: no pronouns, no references like "the user above".
- Keep only durable facts: preferences, decisions, personal or project details, standing instructions.
- Skip transient data (current weather, live prices, one-off lookups) and failed operations.
- If nothing is worth keeping, return an empty list.

Output STRICT JSON, exactly this shape:
{"facts": ["<fact 1>", "<fact 2>"]}
or, when nothing qualifies:
{"facts": []}

User query:
%s

Assistant answer:
%s`

// maybeExtractMemories fires the extraction pass in the background when the
// feature is enabled and the turn looks substantial enough to bother.
func (s *Service) maybeExtractMemories(sid int, query, answer string) {
	if !s.AutoMemoryExtract {
		return
	}
	if len(strings.TrimSpace(answer)) < s.AutoMemoryMinAnswerLen {
		return
	}
	go func() {
		// Nothing above this detached goroutine can recover it, so a panic in
		// the LLM client or the vector store would take the gateway down long
		// after the answer it belongs to was delivered — with no request left
		// to blame. Auto-memory is best-effort by design; failing quietly is
		// exactly the contract.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("auto-memory: extraction panicked: %v\n%s", r, debug.Stack())
			}
		}()
		s.extractAndStoreMemories(sid, query, answer)
	}()
}

// extractAndStoreMemories runs one LLM call to distil the turn into facts and
// stores each through Memory.AddMemory.  Errors are logged, never surfaced —
// memory extraction must not affect the answer the user already received.
func (s *Service) extractAndStoreMemories(sid int, query, answer string) {
	maxFacts := s.AutoMemoryMaxFacts
	if maxFacts <= 0 {
		maxFacts = 5
	}
	prompt := fmt.Sprintf(extractionPromptTemplate, maxFacts,
		truncate(query, 4000), truncate(answer, 8000))

	raw, _, err := s.Chat.CompleteJSON([]llm.Message{{Role: "user", Content: prompt}})
	if err != nil {
		log.Printf("auto-memory: llm call failed: %v", err)
		return
	}
	facts := parseExtractedFacts(raw)
	if len(facts) > maxFacts {
		facts = facts[:maxFacts]
	}
	for _, fact := range facts {
		_, err := s.Memory.AddMemory(fact, map[string]any{
			"source":     "auto-extract",
			"session_id": sid,
		})
		if err != nil {
			log.Printf("auto-memory: store failed: %v", err)
		}
	}
}

var jsonArrayRE = regexp.MustCompile(`(?s)\[.*\]`)

// parseExtractedFacts pulls the facts list out of the model response.  It
// accepts the requested {"facts": [...]} envelope, a bare JSON array, or an
// array embedded in surrounding noise — mirroring the lenient parsing the
// main loop applies to tool-call envelopes.
func parseExtractedFacts(raw string) []string {
	t := strings.TrimSpace(raw)

	var envelope struct {
		Facts []string `json:"facts"`
	}
	if err := json.Unmarshal([]byte(t), &envelope); err == nil && envelope.Facts != nil {
		return cleanFacts(envelope.Facts)
	}

	var arr []string
	if err := json.Unmarshal([]byte(t), &arr); err == nil {
		return cleanFacts(arr)
	}
	if m := jsonArrayRE.FindString(t); m != "" {
		if err := json.Unmarshal([]byte(m), &arr); err == nil {
			return cleanFacts(arr)
		}
	}
	return nil
}

func cleanFacts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, f := range in {
		if t := strings.TrimSpace(f); t != "" {
			out = append(out, t)
		}
	}
	return out
}

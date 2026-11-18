package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// promptLLM records the system prompt of every call and replays responses.
type promptLLM struct {
	mockLLM
	pmu     sync.Mutex
	prompts []string
}

func (p *promptLLM) CompleteJSON(msgs []llm.Message) (string, float64, error) {
	p.pmu.Lock()
	if len(msgs) > 0 {
		p.prompts = append(p.prompts, msgs[0].Content)
	}
	p.pmu.Unlock()
	return p.mockLLM.CompleteJSON(msgs)
}

func TestLexicalRankingPrefersMatchingSkill(t *testing.T) {
	cands := []skills.Skill{
		{Name: "weather", Description: "Current weather for a city"},
		{Name: "tls_probe", Description: "Inspect a server's TLS certificate: issuer, expiry, chain"},
		{Name: "dns_lookup", Description: "Resolve DNS records for a domain"},
	}
	var ix *ToolIndex // nil index: lexical only
	ranked, mode := ix.Rank(context.Background(), "when does the certificate of example.com expire?", cands)
	if mode != "lexical" {
		t.Fatalf("mode = %q", mode)
	}
	if ranked[0].Name != "tls_probe" {
		t.Fatalf("top = %v", ranked)
	}
	ranked, _ = ix.Rank(context.Background(), "dns records for example.org", cands)
	if ranked[0].Name != "dns_lookup" {
		t.Fatalf("top = %v", ranked)
	}
}

// Fake embeddings: one dimension per keyword, so cosine is predictable.
func fakeEmbed(text string) ([]float32, error) {
	t := strings.ToLower(text)
	v := []float32{0.01, 0.01, 0.01}
	if strings.Contains(t, "up") || strings.Contains(t, "health") || strings.Contains(t, "status") {
		v[0] = 1
	}
	if strings.Contains(t, "rain") || strings.Contains(t, "weather") {
		v[1] = 1
	}
	if strings.Contains(t, "money") || strings.Contains(t, "currency") {
		v[2] = 1
	}
	return v, nil
}

func TestEmbeddingRankingAndCache(t *testing.T) {
	var calls int
	var mu sync.Mutex
	ix := &ToolIndex{Embed: func(s string) ([]float32, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return fakeEmbed(s)
	}}
	cands := []skills.Skill{
		{Name: "http_health", Description: "Check whether a URL answers (health / status)"},
		{Name: "weather", Description: "Current weather for a city"},
		{Name: "currency_convert", Description: "Convert money between currencies"},
	}
	ranked, mode := ix.Rank(context.Background(), "will it rain tomorrow?", cands)
	if mode != "embed+lexical" || ranked[0].Name != "weather" {
		t.Fatalf("mode=%s ranked=%v", mode, ranked)
	}
	first := calls
	if first != 4 { // 3 skills + query
		t.Fatalf("embed calls = %d, want 4", first)
	}
	ix.Rank(context.Background(), "is my site up?", cands)
	if calls != first+1 {
		t.Fatalf("skills were re-embedded: %d calls", calls-first)
	}
}

func TestEmbeddingFailureFallsBackToLexical(t *testing.T) {
	ix := &ToolIndex{Embed: func(string) ([]float32, error) { return nil, errors.New("down") }}
	_, mode := ix.Rank(context.Background(), "weather", []skills.Skill{{Name: "weather"}})
	if mode != "lexical" {
		t.Fatalf("mode = %q", mode)
	}
	// Backoff: the next call must not try again.
	ix.Embed = func(string) ([]float32, error) { t.Fatal("embedding retried during backoff"); return nil, nil }
	ix.Rank(context.Background(), "weather", []skills.Skill{{Name: "weather"}})
}

func TestRetrievalTrimsPromptAndFindToolsExpands(t *testing.T) {
	chat := &promptLLM{mockLLM: mockLLM{responses: []string{
		`{"type":"tool_call","tool":"find_tools","args":{"query":"qr code image"},"why":"need a skill"}`,
		`{"type":"final","answer":"ok"}`,
	}}}
	svc := newMinimalService(t, chat)
	svc.ToolTopK = 3
	svc.Index = &ToolIndex{}

	res, err := svc.Run(RunRequest{Query: "what is the weather in Paris?", MaxSteps: 4})
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta["tool_retrieval"] != "lexical" {
		t.Fatalf("meta = %v", res.Meta)
	}
	p := chat.prompts[0]
	if !strings.Contains(p, "RELEVANT SKILLS") || !strings.Contains(p, "- weather(") {
		t.Fatalf("weather not offered:\n%s", p)
	}
	if strings.Contains(p, "- qr_generate(") {
		t.Fatalf("irrelevant skill offered:\n%s", p)
	}
	if !strings.Contains(p, "- skill_creator(") {
		t.Fatalf("pinned skill missing")
	}
	// The static rule block precedes the per-request list (prompt caching).
	if strings.Index(p, "CRITICAL RULES") > strings.Index(p, "RELEVANT SKILLS") {
		t.Fatal("skill list is not at the end of the prompt")
	}
	if n, _ := res.Meta["tools_offered"].(int); n < 4 {
		t.Fatalf("tools_offered = %v", res.Meta["tools_offered"])
	}
}

func TestFindToolsReturnsMatches(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{})
	svc.ToolTopK = 3
	svc.Index = &ToolIndex{}
	st, err := svc.startSession(context.Background(), RunRequest{Query: "hello there", MaxSteps: 1})
	if err != nil {
		t.Fatal(err)
	}
	before := len(st.offered)
	out, _ := st.tools[findToolsName].Execute(context.Background(), map[string]any{"query": "generate a QR code"})
	m := out.(map[string]any)
	tools := m["tools"].([]map[string]any)
	if len(tools) == 0 || tools[0]["name"] != "qr_generate" {
		t.Fatalf("find_tools = %v", m)
	}
	if !st.offered["qr_generate"] || len(st.offered) <= before {
		t.Fatal("found skill was not added to the offered set")
	}
}

func TestRetrievalOffWhenCatalogueSmall(t *testing.T) {
	chat := &promptLLM{mockLLM: mockLLM{responses: []string{`{"type":"final","answer":"ok"}`}}}
	svc := newMinimalService(t, chat)
	svc.ToolTopK = 500
	res, err := svc.Run(RunRequest{Query: "hi", MaxSteps: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta["tool_retrieval"] != "off" || strings.Contains(chat.prompts[0], "RELEVANT SKILLS") {
		t.Fatalf("retrieval should be off: %v", res.Meta)
	}
}

// A noisy embedding model (unrelated vectors) must not push a strong
// keyword match out of the top results.
func TestNoisyEmbeddingsKeepKeywordHit(t *testing.T) {
	ix := &ToolIndex{Embed: func(s string) ([]float32, error) {
		h := float32(len(s)%7) + 1
		return []float32{h, 1 / h, h * h}, nil
	}}
	var cands []skills.Skill
	for i := 0; i < 40; i++ {
		cands = append(cands, skills.Skill{Name: fmt.Sprintf("filler_%02d", i), Description: fmt.Sprintf("unrelated helper number %d", i)})
	}
	cands = append(cands, skills.Skill{Name: "tls_probe", Description: "Probe a TLS endpoint: certificate chain, expiry"})
	ranked, mode := ix.Rank(context.Background(), "check the TLS certificate expiry of example.com", cands)
	if mode != "embed+lexical" {
		t.Fatal(mode)
	}
	for i := 0; i < 5; i++ {
		if ranked[i].Name == "tls_probe" {
			return
		}
	}
	t.Fatalf("tls_probe not in top 5: %v", ranked[:5])
}

func TestQueryHintsFindMathAndNetworkTools(t *testing.T) {
	cands := []skills.Skill{
		{Name: "math_eval", Description: "Safe expression evaluator"},
		{Name: "ip_info", Description: "Geolocation for an IP address"},
		{Name: "weather", Description: "Current weather for a city"},
		{Name: "whois_lookup", Description: "Perform a WHOIS lookup for a domain"},
	}
	var ix *ToolIndex
	for q, want := range map[string]string{
		"what is 2+2*3?":        "math_eval",
		"where is 8.8.8.8?":     "ip_info",
		"who owns example.org?": "whois_lookup",
		"weather in Lisbon":     "weather",
	} {
		ranked, _ := ix.Rank(context.Background(), q, cands)
		if ranked[0].Name != want {
			t.Errorf("%q → %v, want %s", q, ranked[0], want)
		}
	}
}

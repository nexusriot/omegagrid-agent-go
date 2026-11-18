package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// ToolIndex ranks skills against a query so a run's prompt carries only the
// skills that matter for it (tool retrieval). Listing every skill on every
// turn made each call cost more as the catalogue grew, and small local models
// pick the wrong tool far more often out of sixty than out of a dozen.
//
// Scoring is hybrid: a lexical overlap score (always available, no network)
// plus, when an embeddings backend answers, the cosine similarity between the
// query and each skill's description. Skill embeddings are cached by the hash
// of the text they were computed from, so an edited markdown skill is
// re-embedded and an unchanged one never is.
type ToolIndex struct {
	// Embed returns the embedding of a text; nil means lexical-only ranking.
	Embed func(text string) ([]float32, error)

	mu    sync.Mutex
	cache map[string][]float32 // sha256(doc) → embedding
	// downUntil pauses embedding after a failure so an unreachable backend
	// does not add its timeout to every run.
	downUntil time.Time
}

// rankedTool is one scored skill.
type rankedTool struct {
	Name  string
	Score float64
}

// embedTimeout bounds the embedding work one ranking may do (the first run
// embeds the whole catalogue; later runs only the query).
const embedTimeout = 8 * time.Second

// embedBackoff is how long ranking stays lexical after an embedding failure.
const embedBackoff = 2 * time.Minute

// toolDoc is the text a skill is matched on: its name (with underscores as
// spaces, so "dns_lookup" matches "dns"), description and parameter names.
func toolDoc(s skills.Skill) string {
	var b strings.Builder
	b.WriteString(strings.ReplaceAll(s.Name, "_", " "))
	b.WriteString(": ")
	b.WriteString(s.Description)
	if len(s.Parameters) > 0 {
		names := make([]string, 0, len(s.Parameters))
		for k := range s.Parameters {
			names = append(names, k)
		}
		sort.Strings(names)
		b.WriteString(" (params: ")
		b.WriteString(strings.Join(names, ", "))
		b.WriteString(")")
	}
	return b.String()
}

func docKey(doc string) string {
	h := sha256.Sum256([]byte(doc))
	return hex.EncodeToString(h[:])
}

// Rank scores every candidate against query and returns them best first.
// mode reports how the scores were computed: "embed+lexical" or "lexical".
func (ix *ToolIndex) Rank(ctx context.Context, query string, cands []skills.Skill) (ranked []rankedTool, mode string) {
	qTok := tokenize(query + " " + queryHints(query))
	lex := make([]float64, len(cands))
	for i, s := range cands {
		lex[i] = lexicalScore(qTok, s)
	}

	cos := ix.cosines(ctx, query, cands)
	mode = "lexical"
	ranked = make([]rankedTool, len(cands))
	if cos == nil {
		for i, s := range cands {
			ranked[i] = rankedTool{Name: s.Name, Score: lex[i]}
		}
	} else {
		mode = "embed+lexical"
		// Reciprocal-rank fusion: each signal contributes by its rank, not
		// its raw value, so neither a noisy embedding model (cosines bunched
		// in a narrow band) nor a lucky keyword can drown the other. A skill
		// with no keyword overlap gets no lexical share at all.
		const k = 10.0
		er := ranks(cos)
		lr := ranks(lex)
		for i, s := range cands {
			score := 1 / (k + float64(er[i]))
			if lex[i] > 0 {
				score += 1 / (k + float64(lr[i]))
			}
			ranked[i] = rankedTool{Name: s.Name, Score: score}
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Name < ranked[j].Name
	})
	return ranked, mode
}

// ranks returns each value's 1-based rank, highest value first.
func ranks(vals []float64) []int {
	idx := make([]int, len(vals))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return vals[idx[a]] > vals[idx[b]] })
	out := make([]int, len(vals))
	for r, i := range idx {
		out[i] = r + 1
	}
	return out
}

// cosines returns the cosine similarity of query to every candidate, or nil
// when embeddings are unavailable (no backend, an error, or the time budget
// ran out before every skill had an embedding).
func (ix *ToolIndex) cosines(ctx context.Context, query string, cands []skills.Skill) []float64 {
	if ix == nil || ix.Embed == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	ix.mu.Lock()
	if time.Now().Before(ix.downUntil) {
		ix.mu.Unlock()
		return nil
	}
	if ix.cache == nil {
		ix.cache = map[string][]float32{}
	}
	keys := make([]string, len(cands))
	var missing []int
	for i, s := range cands {
		keys[i] = docKey(toolDoc(s))
		if _, ok := ix.cache[keys[i]]; !ok {
			missing = append(missing, i)
		}
	}
	ix.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()

	// Embed the query and any skills not seen before, a few at a time.
	type job struct {
		idx  int // -1 = the query
		text string
	}
	jobs := []job{{-1, query}}
	for _, i := range missing {
		jobs = append(jobs, job{i, toolDoc(cands[i])})
	}
	var (
		qEmb   []float32
		failed bool
		wmu    sync.Mutex
		wg     sync.WaitGroup
	)
	sem := make(chan struct{}, 4)
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wmu.Lock()
				failed = true
				wmu.Unlock()
				return
			}
			defer func() { <-sem }()
			emb, err := ix.Embed(j.text)
			wmu.Lock()
			defer wmu.Unlock()
			if err != nil || len(emb) == 0 || ctx.Err() != nil {
				failed = true
				return
			}
			if j.idx < 0 {
				qEmb = emb
				return
			}
			ix.mu.Lock()
			ix.cache[keys[j.idx]] = emb
			ix.mu.Unlock()
		}(j)
	}
	wg.Wait()
	if failed || qEmb == nil {
		ix.mu.Lock()
		ix.downUntil = time.Now().Add(embedBackoff)
		ix.mu.Unlock()
		return nil
	}

	out := make([]float64, len(cands))
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for i := range cands {
		emb := ix.cache[keys[i]]
		if len(emb) != len(qEmb) {
			// The embedding model changed under us: start over next run.
			ix.cache = map[string][]float32{}
			return nil
		}
		out[i] = cosine(qEmb, emb)
	}
	return out
}

// Warm embeds the catalogue in the background so the first user query does
// not pay for it. Errors are ignored: ranking falls back to lexical.
func (ix *ToolIndex) Warm(ctx context.Context, cands []skills.Skill) {
	if ix == nil || ix.Embed == nil || len(cands) == 0 {
		return
	}
	ix.cosines(ctx, "warm-up", cands)
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Entity patterns that say which kind of tool a query wants even when it has
// no useful words: "what is 2+2*3?", "1.1.1.1?", "example.com".
var (
	arithRE  = regexp.MustCompile(`\d\s*[-+*/^%]\s*\(?\s*\d|\bsqrt\(|\d+\s*!|(?i)\d\s*(?:%|percent)\s+of\s+\d`)
	urlRE    = regexp.MustCompile(`(?i)\bhttps?://`)
	ipv4RE   = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	emailRE  = regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}\b`)
	domainRE = regexp.MustCompile(`(?i)\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,}\b`)
)

// queryHints adds words for the entities in a query, so lexical ranking has
// something to match when the query is mostly numbers or names.
func queryHints(q string) string {
	var h []string
	if arithRE.MatchString(q) {
		h = append(h, "math expression calculate evaluate")
	}
	if urlRE.MatchString(q) {
		h = append(h, "url http")
	}
	if ipv4RE.MatchString(q) {
		h = append(h, "ip address")
	}
	if emailRE.MatchString(q) {
		h = append(h, "email")
	} else if domainRE.MatchString(q) && !ipv4RE.MatchString(q) {
		h = append(h, "domain")
	}
	return strings.Join(h, " ")
}

// stopwords are dropped from both sides of the lexical match: they appear in
// almost every description and would otherwise favour long ones.
var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true, "to": true, "in": true,
	"on": true, "for": true, "with": true, "is": true, "are": true, "be": true, "it": true, "its": true,
	"by": true, "as": true, "at": true, "from": true, "this": true, "that": true, "me": true, "my": true,
	"i": true, "you": true, "your": true, "please": true, "can": true, "what": true, "how": true,
	"do": true, "does": true, "default": true, "e": true, "g": true, "eg": true, "use": true,
	"get": true, "show": true, "tell": true, "give": true, "about": true, "into": true, "not": true,
}

// tokenize lower-cases text and splits it into word tokens (letters/digits),
// dropping stopwords and one-letter tokens.
func tokenize(text string) []string {
	f := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := f[:0]
	for _, t := range f {
		if len(t) < 2 || stopwords[t] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// lexicalScore is the share of distinct query tokens found in the skill's
// name (weight 2) or description/parameters (weight 1), normalised to 0..1.
// A token also matches by prefix (≥4 chars) so "certificates" hits "cert".
func lexicalScore(qTok []string, s skills.Skill) float64 {
	if len(qTok) == 0 {
		return 0
	}
	name := map[string]bool{}
	for _, t := range tokenize(strings.ReplaceAll(s.Name, "_", " ")) {
		name[t] = true
	}
	body := map[string]bool{}
	for _, t := range tokenize(toolDoc(s)) {
		body[t] = true
	}
	seen := map[string]bool{}
	var score, max float64
	for _, q := range qTok {
		if seen[q] {
			continue
		}
		seen[q] = true
		max += 2
		switch {
		case name[q] || prefixHit(q, name):
			score += 2
		case body[q] || prefixHit(q, body):
			score++
		}
	}
	if max == 0 {
		return 0
	}
	return score / max
}

// prefixHit is a poor man's stemmer: q matches a token that is a prefix of
// it or extends it ("cert" / "certificates", both ≥ 4 chars), or shares a
// stem of at least 5 characters with it ("expire" / "expiry").
func prefixHit(q string, set map[string]bool) bool {
	if len(q) < 4 {
		return false
	}
	for t := range set {
		if len(t) < 4 {
			continue
		}
		if strings.HasPrefix(q, t) || strings.HasPrefix(t, q) || commonPrefix(q, t) >= 5 {
			return true
		}
	}
	return false
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

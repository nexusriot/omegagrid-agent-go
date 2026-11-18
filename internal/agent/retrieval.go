package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// DefaultPinnedTools are offered on every run even when retrieval trims the
// catalogue: general-purpose tools whose relevance a query rarely spells out.
var DefaultPinnedTools = []string{"web_search", "schedule_task", "skill_creator"}

// findToolsName is the meta-tool that searches the full catalogue.
const findToolsName = "find_tools"

// retrievalAddendum heads the per-request skill list when retrieval is on.
// It sits at the END of the system prompt so the static prefix (rules,
// envelope format) stays byte-identical across runs for prompt caching.
const retrievalAddendum = `

RELEVANT SKILLS for this request (call them like tools). Only the %d skills
most relevant to this request are listed; %d more exist. If none of these fits,
call find_tools(query="<what you need>") to search the full catalogue, then
call the skill it returns. Never claim a capability is missing without
calling find_tools first.
- find_tools(query (required), k (optional)): search all skills by purpose`

// retrievalActive reports whether this catalogue is big enough to trim.
func (s *Service) retrievalActive(skillCount int) bool {
	if s.ToolTopK <= 0 {
		return false
	}
	return skillCount > s.ToolTopK+len(s.pinned())
}

func (s *Service) pinned() []string {
	if s.PinnedTools != nil {
		return s.PinnedTools
	}
	return DefaultPinnedTools
}

// selectTools decides which tools this run shows the model and returns the
// system prompt for that selection. With retrieval off it is exactly the old
// all-skills prompt.
func (s *Service) selectTools(ctx context.Context, st *runState, query string, tail []memory.Message) string {
	if !s.retrievalActive(len(st.skillNames)) {
		return s.buildSystemPrompt(st.tools, st.skillNames)
	}

	st.tools[findToolsName] = s.findToolsSkill(st)
	st.toolNames[findToolsName] = true

	offered := map[string]bool{
		"vector_add": true, "vector_search": true, "session_search": true, findToolsName: true,
	}
	for _, p := range s.pinned() {
		if st.skillNames[p] {
			offered[p] = true
		}
	}
	var cands []skills.Skill
	for name := range st.skillNames {
		if !offered[name] {
			cands = append(cands, st.tools[name].Schema)
		}
	}
	// The query alone misses follow-ups ("and for the other host?"); the
	// last user turns carry the topic.
	ranked, mode := s.Index.Rank(ctx, retrievalQuery(query, tail), cands)
	for i := 0; i < len(ranked) && i < s.ToolTopK; i++ {
		offered[ranked[i].Name] = true
	}
	st.offered = offered
	st.retrieval = mode
	st.noteOffered()
	st.debug = append(st.debug, fmt.Sprintf("[retrieval] mode=%s offered=%d of %d", mode, len(offered), len(st.tools)))
	return s.buildRetrievalPrompt(st)
}

// retrievalQuery joins the query with the two previous user messages.
func retrievalQuery(query string, tail []memory.Message) string {
	parts := []string{query}
	n := 0
	for i := len(tail) - 1; i >= 0 && n < 2; i-- {
		if tail[i].Role == "user" {
			parts = append(parts, truncate(tail[i].Content, 300))
			n++
		}
	}
	return strings.Join(parts, "\n")
}

// buildRetrievalPrompt is buildSystemPrompt for a trimmed catalogue: the rule
// block without a skill section, then the selected skills at the end.
func (s *Service) buildRetrievalPrompt(st *runState) string {
	base := s.buildSystemPrompt(st.tools, nil)
	var names []string
	for name := range st.offered {
		if st.skillNames[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	hidden := len(st.skillNames) - len(names)
	var b strings.Builder
	b.WriteString(base)
	fmt.Fprintf(&b, retrievalAddendum, len(names), hidden)
	for _, name := range names {
		b.WriteString("\n")
		b.WriteString(formatSkillLine(st.tools[name].Schema))
	}
	return b.String()
}

// offeredTools is the tool table the model was shown (all tools when
// retrieval is off).
func (st *runState) offeredTools() map[string]Skill {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.offered == nil {
		return st.tools
	}
	out := make(map[string]Skill, len(st.offered))
	for name := range st.offered {
		if t, ok := st.tools[name]; ok {
			out[name] = t
		}
	}
	return out
}

// findToolsSkill searches the run's whole skill catalogue and adds the hits
// to the offered set, so a native-tools run can call them on its next turn
// (envelope runs could call any skill by name all along).
func (s *Service) findToolsSkill(st *runState) Skill {
	return Skill{
		Schema: skills.Skill{Name: findToolsName,
			Description: "Search the full skill catalogue by purpose and make the matches callable",
			Parameters: map[string]skills.Param{
				"query": {Type: "string", Description: "What you need to do, e.g. 'check TLS certificate expiry'", Required: true},
				"k":     {Type: "number", Description: "How many skills to return (default 6, max 15)"},
			}},
		Execute: func(ctx context.Context, args map[string]any) (any, error) {
			query, _ := args["query"].(string)
			if strings.TrimSpace(query) == "" {
				return map[string]any{"error": "query is required"}, nil
			}
			k := int(asFloat(args["k"]))
			if k <= 0 {
				k = 6
			}
			if k > 15 {
				k = 15
			}
			var cands []skills.Skill
			for name := range st.skillNames {
				cands = append(cands, st.tools[name].Schema)
			}
			ranked, _ := s.Index.Rank(ctx, query, cands)
			if len(ranked) > k {
				ranked = ranked[:k]
			}
			found := make([]map[string]any, 0, len(ranked))
			st.mu.Lock()
			for _, r := range ranked {
				sch := st.tools[r.Name].Schema
				params := map[string]any{}
				for pn, p := range sch.Parameters {
					params[pn] = map[string]any{"type": p.Type, "required": p.Required, "description": p.Description}
				}
				entry := map[string]any{"name": sch.Name, "description": sch.Description, "parameters": params}
				if sch.Body != "" {
					entry["instructions"] = truncate(sch.Body, 600)
				}
				found = append(found, entry)
				if st.offered != nil {
					st.offered[r.Name] = true
				}
			}
			st.mu.Unlock()
			if st.native {
				specs := toolSpecs(st.offeredTools())
				st.mu.Lock()
				st.toolSpecs = specs
				st.mu.Unlock()
			}
			return map[string]any{
				"count": len(found),
				"tools": found,
				"note":  "These skills are now callable. Call the best match directly with its parameters.",
			}, nil
		},
	}
}

// WarmToolIndex embeds the skill catalogue in the background so the first
// query does not wait for it. A no-op when retrieval is off.
func (s *Service) WarmToolIndex(ctx context.Context) {
	if s.Index == nil || s.Skills == nil {
		return
	}
	list, err := s.Skills.List()
	if err != nil || !s.retrievalActive(len(list)+len(s.NativeSkills)) {
		return
	}
	for _, n := range s.NativeSkills {
		list = append(list, n.Schema)
	}
	s.Index.Warm(ctx, list)
}

// RankedTool is one entry of RankTools.
type RankedTool struct {
	Name    string  `json:"name"`
	Score   float64 `json:"score"`
	Offered bool    `json:"offered"` // within ToolTopK: a run would list it
}

// ToolRanking is what a run with this query would be offered.
type ToolRanking struct {
	Query  string       `json:"query"`
	Mode   string       `json:"mode"` // embed+lexical | lexical | off
	TopK   int          `json:"top_k"`
	Pinned []string     `json:"pinned"`
	Ranked []RankedTool `json:"ranked"`
}

// RankTools ranks the catalogue for query exactly as a new run would (minus
// the conversation tail), for GET /api/tools/rank and retrieval tuning.
// limit caps the returned list (<= 0: all).
func (s *Service) RankTools(ctx context.Context, query string, limit int) (*ToolRanking, error) {
	out := &ToolRanking{Query: query, Mode: "off", TopK: s.ToolTopK, Pinned: []string{}, Ranked: []RankedTool{}}
	if s.Skills == nil {
		return out, nil
	}
	list, err := s.Skills.List()
	if err != nil {
		return nil, err
	}
	for _, n := range s.NativeSkills {
		list = append(list, n.Schema)
	}
	pinned := map[string]bool{}
	for _, p := range s.pinned() {
		pinned[p] = true
	}
	var cands []skills.Skill
	for _, sk := range list {
		if pinned[sk.Name] {
			out.Pinned = append(out.Pinned, sk.Name)
			continue
		}
		cands = append(cands, sk)
	}
	sort.Strings(out.Pinned)
	active := s.retrievalActive(len(list))
	ranked, mode := s.Index.Rank(ctx, query, cands)
	if active {
		out.Mode = mode
	}
	for i, r := range ranked {
		if limit > 0 && i >= limit {
			break
		}
		out.Ranked = append(out.Ranked, RankedTool{Name: r.Name, Score: r.Score, Offered: !active || i < s.ToolTopK})
	}
	return out, nil
}

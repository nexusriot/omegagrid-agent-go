// Package agent contains the tool-calling loop.  It mirrors core/agent.py
// from the original Python project as closely as possible — same system
// prompt, same JSON envelope, same auto-recovery, same streaming events —
// so that existing prompt-engineered skills keep working unchanged.
package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/observability"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// systemPromptTemplate matches core/agent.py exactly so the LLM sees the same
// rules and JSON envelope it has been prompt-engineered against.
const systemPromptTemplate = `You are a compact tool-using agent.

You have tools:
- vector_add(text, meta={...})  -- store a durable fact / decision / preference
- vector_search(query, k=5)      -- semantic search over stored memories
%s

You must ALWAYS output STRICT JSON, in one of the two forms:

A) Tool call:
{
  "type": "tool_call",
  "tool": "<tool_name>",
  "args": { ... },
  "why": "<short reason>"
}

B) Final answer (ONLY after you have all the information you need):
{
  "type": "final",
  "answer": "<plain-text answer to the user>",
  "notes": "<optional constraints/assumptions>"
}

CRITICAL RULES:
- You MUST use the appropriate tool to get real data. NEVER invent, guess, or
  fabricate tool results. If you need weather, time, DNS, HTTP data etc. you
  MUST call the corresponding tool/skill first, then give a final answer based
  on the real tool result.
- NEVER respond with a final answer that contains data you did not obtain from
  a tool call or from the conversation context. If you don't have the data, call
  the tool first.
- Prefer vector_search BEFORE answering questions where prior memory may help.
- Use vector_add to store durable facts, decisions, preferences, or summaries
  the user shares with you.
- In type="final", answer MUST be a human-readable plain-text string
  (not raw JSON, not an object/array). Explain the result to the user naturally.
- If a tool/skill requires an argument the user has not provided (e.g. city for
  weather, host for ping_check), do NOT guess or leave it empty. Instead, return
  a type="final" answer asking the user to provide the missing information.
- Keep tool args minimal and valid.
- SELF-EXTENSION: If the user asks for a capability that NO existing skill covers
  (e.g. "check SSL cert", "convert currency"), use skill_creator to create a new
  skill first, then call it.
- PROMPT-ONLY SKILLS: When a skill result contains skill_type="prompt_only", the
  skill has no external data source — YOU generate the output. Follow the
  "instructions" field directly and immediately return type="final" with your
  generated answer. Do NOT call the same skill again.`

// parallelAddendum is appended to the system prompt when parallel tool calls
// are enabled. It teaches the model the batch envelope format.
const parallelAddendum = `
PARALLEL TOOL CALLS: When you need multiple INDEPENDENT tools whose results do
not depend on each other, you may return all of them in one batch:
{
  "type": "tool_calls",
  "calls": [
    {"tool": "<name>", "args": {...}, "why": "<reason>"},
    {"tool": "<name>", "args": {...}, "why": "<reason>"}
  ]
}
Use this ONLY when the calls are genuinely independent (e.g. weather for two
cities, ping two hosts). For sequential dependencies call tools one at a time.
Maximum %d calls per batch.`

// Attachment is a binary artifact produced by a tool (e.g. a QR code image).
// It is carried through the streaming pipeline and handed to the final consumer
// (Telegram bot, HTTP client, etc.) without going through the LLM.
type Attachment struct {
	Type     string `json:"type"`      // "image"
	Filename string `json:"filename"`  // "qr_code.png"
	MimeType string `json:"mime_type"` // "image/png"
	Base64   string `json:"base64"`    // raw base64 data
}

// Skill is the tool-table entry.  Built-ins (vector_add, vector_search,
// schedule_task) and Python sidecar skills are merged into one map.
type Skill struct {
	Schema  skills.Skill                           // schema for the system prompt
	Execute func(args map[string]any) (any, error) // dispatch
}

// Service is the agent.  It has no per-request state — all dependencies are
// injected and reused across run() and run_stream() calls.
type Service struct {
	Memory          *memory.Client
	Skills          *skills.Client
	Chat            llm.ChatClient
	NativeSkills    map[string]Skill // schedule_task lives here
	ContextTail     int
	MemoryHits      int
	ParallelEnabled bool
	MaxParallel     int
}

// RunRequest is the input contract for both Run and RunStream.
type RunRequest struct {
	Query          string
	SessionID      int
	Remember       bool
	MaxSteps       int
	TelegramChatID *int64
}

// RunResult is what Run returns (mirrors the Python dict).
type RunResult struct {
	SessionID   int                `json:"session_id"`
	Answer      string             `json:"answer"`
	Meta        map[string]any     `json:"meta"`
	Memories    []memory.MemoryHit `json:"memories"`
	DebugLog    string             `json:"debug_log"`
	Attachments []Attachment       `json:"attachments,omitempty"`
}

// Event is one streamed step in RunStream.
type Event struct {
	Event       string         `json:"event"`
	Step        int            `json:"step,omitempty"`
	Tool        string         `json:"tool,omitempty"`
	Args        map[string]any `json:"args,omitempty"`
	Why         string         `json:"why,omitempty"`
	Result      string         `json:"result,omitempty"`
	ElapsedS    float64        `json:"elapsed_s,omitempty"`
	SessionID   int            `json:"session_id,omitempty"`
	Answer      string         `json:"answer,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
	Error       string         `json:"error,omitempty"`
	Attachments []Attachment   `json:"attachments,omitempty"`
}

// Run executes the agent loop end-to-end and returns one final result.
func (s *Service) Run(req RunRequest) (*RunResult, error) {
	state, err := s.startSession(req)
	if err != nil {
		return nil, err
	}

	for step := 1; step <= req.MaxSteps; step++ {
		state.debug = append(state.debug, fmt.Sprintf("[agent] step=%d", step))
		raw, llmS, err := s.Chat.CompleteJSON(state.messages)
		if err != nil {
			return nil, fmt.Errorf("llm complete: %w", err)
		}
		state.timings["llm_chat_s_total"] += llmS
		state.debug = append(state.debug, fmt.Sprintf("[llm] chat_s=%.4f", llmS))
		state.debug = append(state.debug, fmt.Sprintf("[llm] raw=%s", truncate(raw, 300)))

		data, err := parseJSONSafely(raw)
		if err != nil {
			// Treat parse failure as "fallback final answer".
			answer := "I had trouble processing that request. Please try rephrasing."
			_ = s.Memory.AddMessage(state.sid, "assistant", answer)
			return s.fallbackResult(state, answer, step, true), nil
		}
		data = normalizeToolCall(data, state.toolNames)

		respType, _ := data["type"].(string)
		if respType == "final" {
			answer := finalAnswer(data)
			_ = s.Memory.AddMessage(state.sid, "assistant", answer)
			return s.fallbackResult(state, answer, step, false), nil
		}

		// Parallel batch: execute all calls concurrently, then feed combined result.
		if respType == "tool_calls" {
			calls, ok := parseBatchCalls(data)
			if !ok {
				answer := bestAnswer(data)
				_ = s.Memory.AddMessage(state.sid, "assistant", answer)
				return s.fallbackResult(state, answer, step, true), nil
			}
			for i := range calls {
				calls[i].Step = step
			}
			results := s.executeBatch(state, calls)
			for _, r := range results {
				state.attachments = append(state.attachments, r.Atts...)
				_ = s.Memory.AddMessage(state.sid, "tool", r.Result)
				state.timings["skill_s_total"] += r.Elapsed
				state.debug = append(state.debug, fmt.Sprintf("[batch] %s (%.3fs): %s", r.Call.Name, r.Elapsed, truncate(fmt.Sprint(r.Result), 200)))
			}
			batchJSON, _ := json.Marshal(data)
			var allResults []any
			for _, r := range results {
				allResults = append(allResults, r.Result)
			}
			allJSON, _ := json.Marshal(allResults)
			state.messages = append(state.messages,
				llm.Message{Role: "assistant", Content: string(batchJSON)},
				llm.Message{Role: "tool", Content: string(allJSON)},
				llm.Message{Role: "user", Content: batchFollowup(results)},
			)
			continue
		}

		if respType != "tool_call" {
			state.debug = append(state.debug, fmt.Sprintf("[fallback] LLM returned unexpected type=%v", data["type"]))
			answer := bestAnswer(data)
			_ = s.Memory.AddMessage(state.sid, "assistant", answer)
			return s.fallbackResult(state, answer, step, true), nil
		}

		// Single tool call (original path).
		toolName, _ := data["tool"].(string)
		args, _ := data["args"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		why, _ := data["why"].(string)

		isSkill := state.skillNames[toolName]
		kind := "tool"
		if isSkill {
			kind = "skill"
		}
		state.debug = append(state.debug,
			fmt.Sprintf("[%s] >>> CALL %s(%s) reason=%s", kind, toolName, truncateJSON(args, 200), nonempty(why, "-")),
		)

		br := s.runOne(state, batchCall{Name: toolName, Args: args, Why: why, Step: step})
		result := br.Result
		elapsed := br.Elapsed
		state.attachments = append(state.attachments, br.Atts...)

		key := "tool_s_total"
		if isSkill {
			key = "skill_s_total"
		}
		state.timings[key] += elapsed
		state.debug = append(state.debug, fmt.Sprintf("[%s] <<< RESULT (%.3fs): %s", kind, elapsed, truncate(fmt.Sprint(result), 300)))

		_ = s.Memory.AddMessage(state.sid, "tool", result)
		var assistantJSON, toolJSON []byte
		if assistantJSON, err = json.Marshal(data); err != nil {
			assistantJSON = []byte(`{"type":"tool_call"}`)
			state.debug = append(state.debug, fmt.Sprintf("[agent] marshal tool_call: %v", err))
		}
		if toolJSON, err = json.Marshal(result); err != nil {
			toolJSON = []byte(`{"error":"marshal failed"}`)
			state.debug = append(state.debug, fmt.Sprintf("[agent] marshal tool_result: %v", err))
		}
		state.messages = append(state.messages,
			llm.Message{Role: "assistant", Content: string(assistantJSON)},
			llm.Message{Role: "tool", Content: string(toolJSON)},
			llm.Message{Role: "user", Content: toolFollowup(toolName, result)},
		)
	}

	answer := "I could not finish within max_steps. Please refine the goal or increase max_steps."
	_ = s.Memory.AddMessage(state.sid, "assistant", answer)
	res := s.fallbackResult(state, answer, req.MaxSteps, false)
	res.Meta["max_steps_hit"] = true
	return res, nil
}

// RunStream is the streaming counterpart of Run.  Events are pushed onto out
// in real time so the gateway can forward them as SSE.
func (s *Service) RunStream(req RunRequest, out chan<- Event) {
	defer close(out)

	state, err := s.startSession(req)
	if err != nil {
		out <- Event{Event: "error", Error: err.Error()}
		return
	}

	for step := 1; step <= req.MaxSteps; step++ {
		out <- Event{Event: "thinking", Step: step}

		raw, llmS, err := s.Chat.CompleteJSON(state.messages)
		if err != nil {
			out <- Event{Event: "error", Error: err.Error()}
			return
		}
		state.timings["llm_chat_s_total"] += llmS

		data, err := parseJSONSafely(raw)
		if err != nil {
			out <- Event{Event: "error", Error: err.Error()}
			return
		}
		data = normalizeToolCall(data, state.toolNames)

		respType, _ := data["type"].(string)
		if respType == "final" {
			answer := finalAnswer(data)
			_ = s.Memory.AddMessage(state.sid, "assistant", answer)
			out <- Event{
				Event:       "final",
				SessionID:   state.sid,
				Answer:      answer,
				Meta:        s.buildMeta(state, step, false),
				Attachments: state.attachments,
			}
			return
		}
		// Parallel batch.
		if respType == "tool_calls" {
			calls, ok := parseBatchCalls(data)
			if !ok {
				answer := bestAnswer(data)
				_ = s.Memory.AddMessage(state.sid, "assistant", answer)
				meta := s.buildMeta(state, step, false)
				meta["fallback"] = true
				out <- Event{Event: "final", SessionID: state.sid, Answer: answer, Meta: meta, Attachments: state.attachments}
				return
			}
			// Emit tool_call events upfront so the UI shows them immediately.
			for i := range calls {
				calls[i].Step = step
			}
			for _, c := range calls {
				out <- Event{Event: "tool_call", Step: step, Tool: c.Name, Args: c.Args, Why: c.Why}
			}
			results := s.executeBatch(state, calls)
			for _, r := range results {
				state.attachments = append(state.attachments, r.Atts...)
				_ = s.Memory.AddMessage(state.sid, "tool", r.Result)
				state.timings["skill_s_total"] += r.Elapsed
				out <- Event{Event: "tool_result", Step: step, Tool: r.Call.Name, Result: truncate(fmt.Sprint(r.Result), 300), ElapsedS: round3(r.Elapsed)}
			}
			batchJSON, _ := json.Marshal(data)
			var allResults []any
			for _, r := range results {
				allResults = append(allResults, r.Result)
			}
			allJSON, _ := json.Marshal(allResults)
			state.messages = append(state.messages,
				llm.Message{Role: "assistant", Content: string(batchJSON)},
				llm.Message{Role: "tool", Content: string(allJSON)},
				llm.Message{Role: "user", Content: batchFollowup(results)},
			)
			continue
		}

		if respType != "tool_call" {
			answer := bestAnswer(data)
			_ = s.Memory.AddMessage(state.sid, "assistant", answer)
			meta := s.buildMeta(state, step, false)
			meta["fallback"] = true
			out <- Event{
				Event:       "final",
				SessionID:   state.sid,
				Answer:      answer,
				Meta:        meta,
				Attachments: state.attachments,
			}
			return
		}

		// Single tool call.
		toolName, _ := data["tool"].(string)
		args, _ := data["args"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		why, _ := data["why"].(string)
		out <- Event{Event: "tool_call", Step: step, Tool: toolName, Args: args, Why: why}

		br := s.runOne(state, batchCall{Name: toolName, Args: args, Why: why, Step: step})
		result := br.Result
		elapsed := br.Elapsed
		state.attachments = append(state.attachments, br.Atts...)

		key := "tool_s_total"
		if state.skillNames[toolName] {
			key = "skill_s_total"
		}
		state.timings[key] += elapsed

		out <- Event{Event: "tool_result", Step: step, Tool: toolName, Result: truncate(fmt.Sprint(result), 300), ElapsedS: round3(elapsed)}

		_ = s.Memory.AddMessage(state.sid, "tool", result)
		var assistantJSON, toolJSON []byte
		if assistantJSON, err = json.Marshal(data); err != nil {
			assistantJSON = []byte(`{"type":"tool_call"}`)
		}
		if toolJSON, err = json.Marshal(result); err != nil {
			toolJSON = []byte(`{"error":"marshal failed"}`)
		}
		state.messages = append(state.messages,
			llm.Message{Role: "assistant", Content: string(assistantJSON)},
			llm.Message{Role: "tool", Content: string(toolJSON)},
			llm.Message{Role: "user", Content: toolFollowup(toolName, result)},
		)
	}

	answer := "I could not finish within max_steps. Please refine the goal or increase max_steps."
	_ = s.Memory.AddMessage(state.sid, "assistant", answer)
	meta := s.buildMeta(state, req.MaxSteps, false)
	meta["max_steps_hit"] = true
	out <- Event{Event: "final", SessionID: state.sid, Answer: answer, Meta: meta, Attachments: state.attachments}
}

type runState struct {
	sid         int
	timings     map[string]float64
	timer       *observability.Timer
	debug       []string
	messages    []llm.Message
	memories    []memory.MemoryHit
	attachments []Attachment
	tools       map[string]Skill
	skillNames  map[string]bool
	toolNames   map[string]bool // includes built-in tools, used by normalizeToolCall
}

func (s *Service) startSession(req RunRequest) (*runState, error) {
	timer := observability.NewTimer()
	sid := req.SessionID
	if sid == 0 {
		newSid, err := s.Memory.CreateSession()
		if err != nil {
			return nil, fmt.Errorf("create session: %w", err)
		}
		sid = newSid
	}
	st := &runState{
		sid:     sid,
		timer:   timer,
		timings: map[string]float64{},
		debug:   []string{},
	}

	if err := s.Memory.AddMessage(sid, "user", req.Query); err != nil {
		return nil, fmt.Errorf("store user msg: %w", err)
	}
	timer.Mark("sqlite_add_user_s")

	// Initial vector search
	if search, err := s.Memory.SearchMemory(req.Query, s.MemoryHits); err == nil {
		st.memories = search.Hits
		for k, v := range search.Timings {
			st.timings["vector_search_"+k] = v
		}
	} else {
		st.debug = append(st.debug, "[memory] ERROR: vector search failed: "+err.Error())
	}

	// Tail of prior conversation
	tail, err := s.Memory.LoadTail(sid, s.ContextTail)
	if err != nil {
		st.debug = append(st.debug, "[history] ERROR: load tail failed: "+err.Error())
	}
	timer.Mark("sqlite_load_tail_s")

	// Build the tool table from sidecar skills + native skills + vector_*.
	skillList, err := s.Skills.List()
	if err != nil {
		st.debug = append(st.debug, "[skills] WARN: list failed: "+err.Error())
	}
	st.tools = map[string]Skill{}
	st.skillNames = map[string]bool{}
	st.toolNames = map[string]bool{
		"vector_add":    true,
		"vector_search": true,
	}
	st.tools["vector_add"] = Skill{
		Schema: skills.Skill{Name: "vector_add", Description: "Store a durable fact / decision / preference"},
		Execute: func(args map[string]any) (any, error) {
			text, _ := args["text"].(string)
			meta, _ := args["meta"].(map[string]any)
			if meta == nil {
				meta = map[string]any{}
			}
			if _, ok := meta["session_id"]; !ok {
				meta["session_id"] = sid
			}
			res, err := s.Memory.AddMemory(text, meta)
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	}
	st.tools["vector_search"] = Skill{
		Schema: skills.Skill{Name: "vector_search", Description: "Semantic search over stored memories"},
		Execute: func(args map[string]any) (any, error) {
			query, _ := args["query"].(string)
			if query == "" {
				query, _ = args["q"].(string)
			}
			if query == "" {
				return map[string]any{"hits": []any{}, "error": "No query provided"}, nil
			}
			k := 5
			if v, ok := args["k"]; ok {
				k = int(asFloat(v))
				if k <= 0 {
					k = 5
				}
			}
			res, err := s.Memory.SearchMemory(query, k)
			if err != nil {
				return nil, err
			}
			return map[string]any{"hits": res.Hits}, nil
		},
	}

	// Sidecar (Python) skills
	for _, sk := range skillList {
		name := sk.Name
		// Allow native skill (e.g. schedule_task) to override the sidecar version.
		if _, isNative := s.NativeSkills[name]; isNative {
			continue
		}
		skCopy := sk
		st.tools[name] = Skill{
			Schema: skCopy,
			Execute: func(args map[string]any) (any, error) {
				return s.Skills.Execute(skCopy.Name, args)
			},
		}
		st.skillNames[name] = true
		st.toolNames[name] = true
	}
	// Native skills (schedule_task lives here so it talks to the Go scheduler).
	for name, native := range s.NativeSkills {
		st.tools[name] = native
		st.skillNames[name] = true
		st.toolNames[name] = true
	}

	st.debug = append(st.debug, fmt.Sprintf("[init] tools=%v", keys(st.tools)))
	st.debug = append(st.debug, fmt.Sprintf("[init] skills_count=%d", len(st.skillNames)))

	// Assemble the message list
	systemPrompt := s.buildSystemPrompt(st.tools, st.skillNames)
	contextParts := []string{formatMemoryHits(st.memories)}
	if req.TelegramChatID != nil {
		contextParts = append(contextParts, fmt.Sprintf("Current Telegram chat_id: %d (use this for notify_telegram_chat_id when user asks for Telegram notifications)", *req.TelegramChatID))
	}
	st.messages = []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "system", Content: strings.Join(contextParts, "\n")},
	}
	for _, m := range tail {
		st.messages = append(st.messages, llm.Message{Role: m.Role, Content: m.Content})
	}
	st.messages = append(st.messages, llm.Message{Role: "user", Content: req.Query})
	return st, nil
}

func (s *Service) fallbackResult(st *runState, answer string, step int, fallback bool) *RunResult {
	meta := s.buildMeta(st, step, fallback)
	return &RunResult{
		SessionID:   st.sid,
		Answer:      answer,
		Meta:        meta,
		Memories:    st.memories,
		DebugLog:    strings.Join(st.debug, "\n"),
		Attachments: st.attachments,
	}
}

func (s *Service) buildMeta(st *runState, step int, fallback bool) map[string]any {
	for k, v := range st.timer.AsMap() {
		st.timings[k] = v
	}
	out := map[string]any{
		"timings":    st.timings,
		"step_count": step,
		"model":      s.Chat.Model(),
	}
	if fallback {
		out["fallback"] = true
	}
	return out
}

// sensitiveSkills have their args and result redacted in the audit log.
var sensitiveSkills = map[string]bool{
	"password_gen":  true,
	"shell_command": true,
	"ssh_command":   true,
}

func (s *Service) buildSystemPrompt(tools map[string]Skill, skillNames map[string]bool) string {
	var skillSection string
	if len(skillNames) > 0 {
		var lines []string
		lines = append(lines, "", "You also have skills (call them like tools):")
		for name := range tools {
			if !skillNames[name] {
				continue
			}
			lines = append(lines, formatSkillLine(tools[name].Schema))
		}
		skillSection = strings.Join(lines, "\n")
	}
	base := fmt.Sprintf(systemPromptTemplate, skillSection)
	if s.ParallelEnabled {
		maxP := s.MaxParallel
		if maxP <= 0 {
			maxP = 4
		}
		base += fmt.Sprintf(parallelAddendum, maxP)
	}
	return base
}

func formatSkillLine(s skills.Skill) string {
	var params []string
	for k, p := range s.Parameters {
		req := " (optional)"
		if p.Required {
			req = " (required)"
		}
		params = append(params, k+req)
	}
	line := fmt.Sprintf("- %s(%s): %s", s.Name, strings.Join(params, ", "), s.Description)
	if s.Body != "" {
		bodyLines := strings.Split(s.Body, "\n")
		if len(bodyLines) > 5 {
			bodyLines = bodyLines[:5]
		}
		for _, bl := range bodyLines {
			line += "\n    " + strings.TrimSpace(bl)
		}
	}
	return line
}

func formatMemoryHits(hits []memory.MemoryHit) string {
	if len(hits) == 0 {
		return "Relevant memories: (none)"
	}
	var sb strings.Builder
	sb.WriteString("Relevant memories (semantic search):")
	for i, h := range hits {
		var tag string
		if h.Metadata != nil {
			if v, ok := h.Metadata["tag"].(string); ok {
				tag = v
			} else if v, ok := h.Metadata["type"].(string); ok {
				tag = v
			}
		}
		tagStr := ""
		if tag != "" {
			tagStr = "(" + tag + ") "
		}
		sb.WriteString(fmt.Sprintf("\n%d. [distance=%.4f] %s%s", i+1, h.Distance, tagStr, h.Text))
	}
	return sb.String()
}

var jsonObjectRE = regexp.MustCompile(`(?s)\{.*\}`)

// parseJSONSafely accepts either a clean JSON object or one embedded in noise.
// It also unwraps the legacy "raw_model_json" envelope so old history rows
// don't trip up the agent.
func parseJSONSafely(text string) (map[string]any, error) {
	t := strings.TrimSpace(text)
	var data map[string]any
	if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
		if err := json.Unmarshal([]byte(t), &data); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
	} else {
		m := jsonObjectRE.FindString(t)
		if m == "" {
			return nil, fmt.Errorf("model did not return JSON. Got: %s", truncate(t, 300))
		}
		if err := json.Unmarshal([]byte(m), &data); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
	}
	if len(data) == 1 {
		if inner, ok := data["raw_model_json"].(string); ok {
			var nested map[string]any
			if err := json.Unmarshal([]byte(inner), &nested); err == nil {
				data = nested
			}
		}
	}
	return data, nil
}

// normalizeToolCall fixes malformed envelopes where the LLM uses the tool name
// as the "type" field, e.g. {"type":"weather","city":"London"}.  Mirrors the
// Python implementation.
func normalizeToolCall(data map[string]any, knownTools map[string]bool) map[string]any {
	t, _ := data["type"].(string)
	if t == "tool_call" || t == "final" {
		return data
	}
	if knownTools[t] {
		args := map[string]any{}
		for k, v := range data {
			if k == "type" || k == "why" {
				continue
			}
			args[k] = v
		}
		why, _ := data["why"].(string)
		if why == "" {
			why = "auto-recovered"
		}
		return map[string]any{"type": "tool_call", "tool": t, "args": args, "why": why}
	}
	if name, ok := data["tool"].(string); ok && knownTools[name] {
		data["type"] = "tool_call"
		if _, hasArgs := data["args"]; !hasArgs {
			args := map[string]any{}
			for k, v := range data {
				if k == "type" || k == "tool" || k == "why" {
					continue
				}
				args[k] = v
			}
			data["args"] = args
		}
		return data
	}
	return data
}

func finalAnswer(data map[string]any) string {
	if a, ok := data["answer"].(string); ok {
		return a
	}
	if a, ok := data["answer"]; ok && a != nil {
		b, _ := json.MarshalIndent(a, "", "  ")
		return string(b)
	}
	return ""
}

func bestAnswer(data map[string]any) string {
	for _, k := range []string{"answer", "text", "result"} {
		if v, ok := data[k].(string); ok && v != "" {
			return v
		}
	}
	return "I had trouble processing that request. Please try rephrasing."
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func truncateJSON(v any, n int) string {
	b, _ := json.Marshal(v)
	return truncate(string(b), n)
}

func nonempty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func asFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

// toolFollowup returns the user turn appended after each tool result.
// When the result contains an error key the message explicitly forbids the
// LLM from claiming success, preventing hallucinated "I stored it" replies.
func toolFollowup(toolName string, result any) string {
	if m, ok := result.(map[string]any); ok {
		if errMsg, _ := m["error"].(string); errMsg != "" {
			return fmt.Sprintf(
				"Tool %q FAILED with error: %s\n"+
					"You MUST tell the user that this operation failed. "+
					"Do NOT claim it succeeded.",
				toolName, errMsg,
			)
		}
	}
	return `Tool result received. If you now have all the information needed, return type="final". Otherwise call another tool.`
}

// batchCall is one entry in a tool_calls batch.
type batchCall struct {
	Name string
	Args map[string]any
	Why  string
	Step int
}

// batchResult is the outcome of executing one batchCall.
type batchResult struct {
	Call    batchCall
	Result  any
	Elapsed float64
	Atts    []Attachment
}

// executeBatch runs multiple tool calls. When s.ParallelEnabled is true and
// there is more than one call, they execute concurrently up to s.MaxParallel.
func (s *Service) executeBatch(state *runState, calls []batchCall) []batchResult {
	out := make([]batchResult, len(calls))
	if !s.ParallelEnabled || len(calls) == 1 {
		for i, c := range calls {
			out[i] = s.runOne(state, c)
		}
		return out
	}

	maxP := s.MaxParallel
	if maxP <= 0 {
		maxP = 4
	}
	sem := make(chan struct{}, maxP)
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func(i int, c batchCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = s.runOne(state, c)
		}(i, c)
	}
	wg.Wait()
	return out
}

// runOne executes a single tool/skill call, writes a best-effort audit
// record (covering single, batch and parallel calls), and returns its result.
func (s *Service) runOne(state *runState, c batchCall) batchResult {
	kind := "unknown"
	if _, ok := state.tools[c.Name]; ok {
		if state.skillNames[c.Name] {
			kind = "skill"
		} else {
			kind = "tool"
		}
	}

	entry, ok := state.tools[c.Name]
	var result any
	var execErr error
	var dur time.Duration
	if ok {
		t0 := time.Now()
		r, err := entry.Execute(c.Args)
		dur = time.Since(t0)
		if err != nil {
			execErr = err
			result = map[string]any{"error": err.Error(), "tool": c.Name, "args": c.Args}
		} else {
			result = r
		}
	} else {
		result = map[string]any{"error": "Unknown tool/skill: " + c.Name, "available": keys(state.tools)}
	}
	elapsed := dur.Seconds()
	atts, cleaned := ExtractAttachments(c.Name, result)

	auditArgs := any(c.Args)
	auditResult := any(cleaned)
	if sensitiveSkills[c.Name] {
		auditArgs = map[string]any{"redacted": true}
		auditResult = map[string]any{"redacted": true}
	}
	errMsg := ""
	if execErr != nil {
		errMsg = execErr.Error()
	}
	_ = s.Memory.AddInvocation(memory.AuditRecord{
		SessionID:  state.sid,
		Step:       c.Step,
		Skill:      c.Name,
		Kind:       kind,
		Args:       auditArgs,
		Result:     auditResult,
		ErrorMsg:   errMsg,
		DurationMS: dur.Milliseconds(),
		Why:        c.Why,
	})

	return batchResult{Call: c, Result: cleaned, Elapsed: elapsed, Atts: atts}
}

// parseBatchCalls extracts the calls array from a tool_calls envelope.
func parseBatchCalls(data map[string]any) ([]batchCall, bool) {
	raw, ok := data["calls"]
	if !ok {
		return nil, false
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]batchCall, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["tool"].(string)
		if name == "" {
			continue
		}
		args, _ := m["args"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		why, _ := m["why"].(string)
		out = append(out, batchCall{Name: name, Args: args, Why: why})
	}
	return out, len(out) > 0
}

// batchFollowup builds the user follow-up message after a parallel batch.
func batchFollowup(results []batchResult) string {
	var names []string
	for _, r := range results {
		names = append(names, r.Call.Name)
	}
	var sb strings.Builder
	sb.WriteString("Tool results received for: ")
	sb.WriteString(strings.Join(names, ", "))
	sb.WriteString(".\n")
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", r.Call.Name, truncate(fmt.Sprint(r.Result), 200)))
	}
	sb.WriteString(`If you now have all the information needed, return type="final". Otherwise call another tool.`)
	return sb.String()
}

// ExtractAttachments inspects a tool result for binary artifacts (images) and
// returns them as Attachment values.  The original result map is modified
// in-place: the heavy base64 payload is replaced with a short human-readable
// summary so the LLM context doesn't blow up.
func ExtractAttachments(toolName string, result any) ([]Attachment, any) {
	m, ok := result.(map[string]any)
	if !ok {
		return nil, result
	}

	b64, hasB64 := m["image_base64"].(string)
	if !hasB64 || b64 == "" {
		return nil, result
	}

	format, _ := m["image_format"].(string)
	if format == "" {
		format = "png"
	}
	mime := "image/" + format
	filename := toolName + "." + format

	sizeBytes := 0
	if v, ok := m["size_bytes"]; ok {
		sizeBytes = int(asFloat(v))
	}

	att := Attachment{
		Type:     "image",
		Filename: filename,
		MimeType: mime,
		Base64:   b64,
	}

	// Replace the heavy fields with a compact summary for the LLM context.
	delete(m, "image_base64")
	delete(m, "data_uri")
	m["_image_attached"] = true
	if sizeBytes > 0 {
		m["_image_summary"] = fmt.Sprintf("%s image generated (%s, %d bytes) — will be delivered to user as a file", toolName, format, sizeBytes)
	} else {
		m["_image_summary"] = fmt.Sprintf("%s image generated (%s) — will be delivered to user as a file", toolName, format)
	}

	return []Attachment{att}, m
}

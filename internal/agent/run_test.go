package agent

import (
	"encoding/base64"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

// recordingSkill is a native skill that remembers how it was called.
type recordingSkill struct {
	mu     sync.Mutex
	calls  []map[string]any
	result any
	err    error
}

func (r *recordingSkill) native(name string) Skill {
	return Skill{
		Schema: skills.Skill{
			Name:        name,
			Description: "test skill",
			Parameters:  map[string]skills.Param{"x": {Type: "string", Required: true}},
		},
		Execute: func(args map[string]any) (any, error) {
			r.mu.Lock()
			r.calls = append(r.calls, args)
			r.mu.Unlock()
			return r.result, r.err
		},
	}
}

func (r *recordingSkill) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestRunSingleToolCallThenFinal(t *testing.T) {
	rec := &recordingSkill{result: map[string]any{"temp_c": 20}}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"weather","args":{"city":"Berlin"},"why":"user asked"}`,
		`{"type":"final","answer":"It is 20C in Berlin."}`,
	}})
	svc.NativeSkills = map[string]Skill{"weather": rec.native("weather")}

	res, err := svc.Run(RunRequest{Query: "weather in Berlin?", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "It is 20C in Berlin." {
		t.Fatalf("answer = %q", res.Answer)
	}
	if res.Meta["step_count"] != 2 {
		t.Fatalf("step_count = %v, want 2", res.Meta["step_count"])
	}
	if res.Meta["fallback"] != nil {
		t.Fatalf("a clean run was marked as a fallback: %v", res.Meta)
	}
	if rec.count() != 1 {
		t.Fatalf("skill called %d times, want 1", rec.count())
	}
	if rec.calls[0]["city"] != "Berlin" {
		t.Fatalf("args = %v", rec.calls[0])
	}

	// Skill time is accounted separately from built-in tool time.
	timings, _ := res.Meta["timings"].(map[string]float64)
	if _, ok := timings["skill_s_total"]; !ok {
		t.Fatalf("timings do not record skill time: %v", timings)
	}
	// The debug log is the operator's only view into the loop.
	for _, want := range []string{"[agent] step=1", "CALL weather", "RESULT"} {
		if !strings.Contains(res.DebugLog, want) {
			t.Fatalf("debug log missing %q:\n%s", want, res.DebugLog)
		}
	}
}

// An unknown tool must come back as a readable error the model can recover
// from, listing what it could have called instead.
func TestRunUnknownToolTellsModelWhatExists(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"does_not_exist","args":{},"why":"guessing"}`,
		`{"type":"final","answer":"Sorry, I cannot do that."}`,
	}})

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "Sorry, I cannot do that." {
		t.Fatalf("answer = %q", res.Answer)
	}
	if !strings.Contains(res.DebugLog, "CALL does_not_exist") {
		t.Fatalf("debug log does not record the unknown call:\n%s", res.DebugLog)
	}

	// The model is told what it could have called instead, in a stable order.
	recs, _, err := svc.Memory.ListInvocations(memory.AuditFilter{})
	if err != nil {
		t.Fatalf("ListInvocations: %v", err)
	}
	if len(recs) != 1 || recs[0].Kind != "unknown" {
		t.Fatalf("audit rows = %+v, want one row of kind \"unknown\"", recs)
	}
	result, _ := recs[0].Result.(map[string]any)
	msg, _ := result["error"].(string)
	if !strings.Contains(msg, "Unknown tool/skill: does_not_exist") {
		t.Fatalf("result does not name the unknown tool: %v", result)
	}
	available, _ := result["available"].([]any)
	if len(available) == 0 {
		t.Fatalf("result lists no alternatives: %v", result)
	}
	var names []string
	for _, a := range available {
		names = append(names, a.(string))
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("available tools are not in a stable order: %v", names)
	}
}

// A skill that errors must be reported as failed, with the follow-up explicitly
// forbidding the model from claiming success.
func TestRunSkillErrorIsSurfacedToModel(t *testing.T) {
	rec := &recordingSkill{err: errFake{}}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"flaky","args":{},"why":"try"}`,
		`{"type":"final","answer":"That failed."}`,
	}})
	svc.NativeSkills = map[string]Skill{"flaky": rec.native("flaky")}

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "That failed." {
		t.Fatalf("answer = %q", res.Answer)
	}

	// The audit log records the failure.
	recs, _, err := svc.Memory.ListInvocations(memory.AuditFilter{})
	if err != nil {
		t.Fatalf("ListInvocations: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(recs))
	}
	if recs[0].ErrorMsg == "" {
		t.Fatalf("audit row records no error: %+v", recs[0])
	}
}

type errFake struct{}

func (errFake) Error() string { return "backend exploded" }

func TestRunMaxStepsExhausted(t *testing.T) {
	// The model never returns a final answer.
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"vector_search","args":{"query":"a"},"why":"1"}`,
		`{"type":"tool_call","tool":"vector_search","args":{"query":"b"},"why":"2"}`,
		`{"type":"tool_call","tool":"vector_search","args":{"query":"c"},"why":"3"}`,
	}})

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Answer, "could not finish within max_steps") {
		t.Fatalf("answer = %q", res.Answer)
	}
	if res.Meta["max_steps_hit"] != true {
		t.Fatalf("meta.max_steps_hit = %v, want true", res.Meta["max_steps_hit"])
	}
	if res.Meta["step_count"] != 2 {
		t.Fatalf("step_count = %v, want the configured max of 2", res.Meta["step_count"])
	}
}

func TestRunUnparseableReplyFallsBack(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{"I am prose, not JSON."}})

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Answer, "trouble processing") {
		t.Fatalf("answer = %q", res.Answer)
	}
	if res.Meta["fallback"] != true {
		t.Fatalf("meta.fallback = %v, want true", res.Meta["fallback"])
	}
}

// An envelope that parses but is neither final nor a tool call falls back to
// the model's best-effort text rather than discarding it.
func TestRunUnrecognisedEnvelopeUsesBestAnswer(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"something_else","text":"here is my prose answer"}`,
	}})

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "here is my prose answer" {
		t.Fatalf("answer = %q", res.Answer)
	}
	if res.Meta["fallback"] != true {
		t.Fatalf("meta.fallback = %v", res.Meta["fallback"])
	}
	if !strings.Contains(res.DebugLog, "unexpected type") {
		t.Fatalf("debug log does not explain the fallback:\n%s", res.DebugLog)
	}
}

// The recovery path for models that put the tool name in "type".
func TestRunRecoversMalformedToolEnvelope(t *testing.T) {
	rec := &recordingSkill{result: map[string]any{"ok": true}}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"weather","city":"Paris"}`,
		`{"type":"final","answer":"recovered"}`,
	}})
	svc.NativeSkills = map[string]Skill{"weather": rec.native("weather")}

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "recovered" {
		t.Fatalf("answer = %q", res.Answer)
	}
	if rec.count() != 1 {
		t.Fatalf("recovered envelope did not reach the skill (%d calls)", rec.count())
	}
	if rec.calls[0]["city"] != "Paris" {
		t.Fatalf("recovered args = %v, want the sibling fields promoted", rec.calls[0])
	}
}

func TestRunBatchExecutesEveryCall(t *testing.T) {
	rec := &recordingSkill{result: map[string]any{"ok": true}}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_calls","calls":[
			{"tool":"probe","args":{"x":"a"},"why":"1"},
			{"tool":"probe","args":{"x":"b"},"why":"2"}
		]}`,
		`{"type":"final","answer":"both done"}`,
	}})
	svc.ParallelEnabled = true
	svc.MaxParallel = 4
	svc.NativeSkills = map[string]Skill{"probe": rec.native("probe")}

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "both done" {
		t.Fatalf("answer = %q", res.Answer)
	}
	if rec.count() != 2 {
		t.Fatalf("skill called %d times, want 2", rec.count())
	}
	if !strings.Contains(res.DebugLog, "[batch] probe") {
		t.Fatalf("debug log has no batch entries:\n%s", res.DebugLog)
	}
}

// A tool_calls envelope with no usable calls must not hang the loop.
func TestRunBatchWithNoUsableCalls(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_calls","calls":[],"answer":"nothing to do"}`,
	}})

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "nothing to do" {
		t.Fatalf("answer = %q", res.Answer)
	}
	if res.Meta["fallback"] != true {
		t.Fatalf("meta.fallback = %v, want true", res.Meta["fallback"])
	}
}

// Image results are lifted out of the LLM context into Attachments; only a
// short summary is fed back to the model.
func TestRunLiftsAttachmentsOutOfContext(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("fake-png-bytes"))
	rec := &recordingSkill{result: map[string]any{
		"image_base64": b64,
		"image_format": "png",
		"size_bytes":   14,
		"data_uri":     "data:image/png;base64," + b64,
	}}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"qr_generate","args":{"data":"x"},"why":"qr"}`,
		`{"type":"final","answer":"here is your code"}`,
	}})
	svc.NativeSkills = map[string]Skill{"qr_generate": rec.native("qr_generate")}

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Attachments) != 1 {
		t.Fatalf("attachments = %v, want 1", res.Attachments)
	}
	att := res.Attachments[0]
	if att.Base64 != b64 || att.MimeType != "image/png" || att.Filename != "qr_generate.png" {
		t.Fatalf("attachment = %+v", att)
	}
	// The heavy payload must not appear in the debug log (a proxy for context).
	if strings.Contains(res.DebugLog, b64) {
		t.Fatal("base64 payload leaked into the LLM-visible transcript")
	}
	if !strings.Contains(res.DebugLog, "_image_summary") && !strings.Contains(res.DebugLog, "image generated") {
		t.Fatalf("no image summary in the transcript:\n%s", res.DebugLog)
	}
}

// Sensitive skills have both args and results redacted in the audit log.
func TestRunRedactsSensitiveSkillsInAudit(t *testing.T) {
	rec := &recordingSkill{result: map[string]any{"passwords": []string{"s3cr3t"}}}
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"password_gen","args":{"length":32},"why":"gen"}`,
		`{"type":"final","answer":"done"}`,
	}})
	svc.NativeSkills = map[string]Skill{"password_gen": rec.native("password_gen")}

	if _, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	recs, _, err := svc.Memory.ListInvocations(memory.AuditFilter{})
	if err != nil {
		t.Fatalf("ListInvocations: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("audit rows = %d", len(recs))
	}
	args, _ := recs[0].Args.(map[string]any)
	result, _ := recs[0].Result.(map[string]any)
	if args["redacted"] != true || result["redacted"] != true {
		t.Fatalf("password_gen was not redacted: args=%v result=%v", recs[0].Args, recs[0].Result)
	}
}

// The Telegram chat id is injected as context so schedule_task can wire up
// "notify me on Telegram" without asking the user for a numeric id.
func TestRunInjectsTelegramChatID(t *testing.T) {
	chat := &capturingLLM{reply: `{"type":"final","answer":"ok"}`}
	svc := newMinimalService(t, chat)

	chatID := int64(4242)
	if _, err := svc.Run(RunRequest{Query: "remind me", MaxSteps: 2, TelegramChatID: &chatID}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var joined strings.Builder
	for _, m := range chat.lastMessages {
		joined.WriteString(m.Content + "\n")
	}
	if !strings.Contains(joined.String(), "4242") {
		t.Fatalf("chat id not injected into the prompt:\n%s", joined.String())
	}
	// The current UTC time is injected too, so relative times can be converted
	// to cron without a datetime_skill round trip.
	if !strings.Contains(joined.String(), "Current UTC datetime:") {
		t.Fatalf("no current time in the prompt:\n%s", joined.String())
	}
}

// capturingLLM records the last message list it was given.
type capturingLLM struct {
	reply        string
	lastMessages []llm.Message
}

func (c *capturingLLM) CompleteJSON(msgs []llm.Message) (string, float64, error) {
	c.lastMessages = msgs
	return c.reply, 0, nil
}
func (c *capturingLLM) Model() string   { return "capturing" }
func (c *capturingLLM) BaseURL() string { return "" }

func TestRunSystemPromptListsSkills(t *testing.T) {
	chat := &capturingLLM{reply: `{"type":"final","answer":"ok"}`}
	svc := newMinimalService(t, chat)
	rec := &recordingSkill{}
	svc.NativeSkills = map[string]Skill{"schedule_task": rec.native("schedule_task")}

	if _, err := svc.Run(RunRequest{Query: "q", MaxSteps: 2}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(chat.lastMessages) < 2 || chat.lastMessages[0].Role != "system" {
		t.Fatalf("first message is not the system prompt: %+v", chat.lastMessages)
	}
	prompt := chat.lastMessages[0].Content
	for _, want := range []string{"vector_add", "vector_search", "STRICT JSON", "schedule_task"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, prompt)
		}
	}
	// The parallel addendum only appears when the feature is on.
	if strings.Contains(prompt, "PARALLEL TOOL CALLS") {
		t.Fatal("parallel addendum present with AGENT_PARALLEL_TOOLS off")
	}

	svc.ParallelEnabled = true
	svc.MaxParallel = 3
	if _, err := svc.Run(RunRequest{Query: "q", MaxSteps: 2}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	prompt = chat.lastMessages[0].Content
	if !strings.Contains(prompt, "PARALLEL TOOL CALLS") {
		t.Fatalf("parallel addendum missing when enabled:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Maximum 3 calls per batch") {
		t.Fatalf("addendum does not state the configured limit:\n%s", prompt)
	}
}

func TestFormatMemoryHits(t *testing.T) {
	if got := formatMemoryHits(nil); got != "Relevant memories: (none)" {
		t.Fatalf("formatMemoryHits(nil) = %q", got)
	}

	got := formatMemoryHits([]memory.MemoryHit{
		{Text: "vlad prefers tabs", Distance: 0.1234, Metadata: map[string]any{"tag": "preference"}},
		{Text: "project uses Go", Distance: 0.5, Metadata: map[string]any{"type": "project"}},
		{Text: "no metadata", Distance: 0.9},
	})
	for _, want := range []string{
		"1. [distance=0.1234] (preference) vlad prefers tabs",
		"2. [distance=0.5000] (project) project uses Go",
		"3. [distance=0.9000] no metadata",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted hits missing %q:\n%s", want, got)
		}
	}
}

func TestRunStoresConversationHistory(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{`{"type":"final","answer":"the answer"}`}})

	res, err := svc.Run(RunRequest{Query: "the question", MaxSteps: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	msgs, err := svc.Memory.ListMessages(res.SessionID, 100, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("stored %d messages, want 2 (user + assistant): %v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "the question" {
		t.Fatalf("message 0 = %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "the answer" {
		t.Fatalf("message 1 = %+v", msgs[1])
	}
}

func TestRunReusesGivenSession(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"final","answer":"a"}`, `{"type":"final","answer":"b"}`,
	}})

	first, err := svc.Run(RunRequest{Query: "one", MaxSteps: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	second, err := svc.Run(RunRequest{Query: "two", SessionID: first.SessionID, MaxSteps: 2})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if second.SessionID != first.SessionID {
		t.Fatalf("session %d != %d", second.SessionID, first.SessionID)
	}
	msgs, _ := svc.Memory.ListMessages(first.SessionID, 100, 0)
	if len(msgs) != 4 {
		t.Fatalf("session has %d messages, want 4", len(msgs))
	}
}

func TestHelpers(t *testing.T) {
	if got := nonempty("", "fallback"); got != "fallback" {
		t.Fatalf("nonempty = %q", got)
	}
	if got := nonempty("value", "fallback"); got != "value" {
		t.Fatalf("nonempty = %q", got)
	}

	if got := truncateJSON(map[string]any{"a": 1}, 100); got != `{"a":1}` {
		t.Fatalf("truncateJSON = %q", got)
	}
	if got := truncateJSON(map[string]any{"key": strings.Repeat("x", 200)}, 20); len(got) > 20 {
		t.Fatalf("truncateJSON returned %d bytes, want <= 20", len(got))
	}

	// asFloat feeds the vector_search "k" argument, which models send as any
	// numeric flavour they like.
	for in, want := range map[any]float64{
		float64(5): 5, int(5): 5, int64(5): 5, "5": 0, nil: 0,
	} {
		if got := asFloat(in); got != want {
			t.Fatalf("asFloat(%#v) = %v, want %v", in, got, want)
		}
	}

	if got := round3(1.23456); got != 1.235 {
		t.Fatalf("round3 = %v, want 1.235", got)
	}
	if got := round3(0); got != 0 {
		t.Fatalf("round3(0) = %v", got)
	}
}

func TestVectorToolsAreWiredToMemory(t *testing.T) {
	// vector_search with no query must answer rather than error, so the loop can
	// continue; vector_add stamps the session id into the metadata.
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"vector_search","args":{},"why":"no query"}`,
		`{"type":"final","answer":"done"}`,
	}})

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.DebugLog, "No query provided") {
		t.Fatalf("debug log does not show the empty-query result:\n%s", res.DebugLog)
	}
	// It is a built-in tool, not a skill, so it bills to tool time.
	timings, _ := res.Meta["timings"].(map[string]float64)
	if _, ok := timings["tool_s_total"]; !ok {
		t.Fatalf("timings do not record tool time: %v", timings)
	}
}

// vector_search accepts "q" as an alias for "query" because models use both.
func TestVectorSearchAcceptsQAlias(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{
		`{"type":"tool_call","tool":"vector_search","args":{"q":"anything"},"why":"alias"}`,
		`{"type":"final","answer":"done"}`,
	}})

	res, err := svc.Run(RunRequest{Query: "q", MaxSteps: 5})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The embeddings backend is unreachable in tests, so the call fails — but it
	// must fail having tried the search, not having rejected the alias.
	if strings.Contains(res.DebugLog, "No query provided") {
		t.Fatalf("the q alias was not recognised:\n%s", res.DebugLog)
	}
}

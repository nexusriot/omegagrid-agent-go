package agent

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
)

func TestTruncateKeepsTextAroundInvalidBytes(t *testing.T) {
	// shell_command stdout and web_scrape of a non-UTF-8 page can carry invalid
	// bytes. Trimming until the whole prefix validated returned "", so the
	// result vanished from debug logs and stream events entirely.
	in := "exit=0 stdout=\xff\xfe done"
	got := truncate(in, len(in)-1)
	if got == "" {
		t.Fatal("truncate dropped the whole string")
	}
	if !strings.Contains(got, "exit=0") {
		t.Fatalf("truncate(%q) = %q, want the leading text kept", in, got)
	}
}

func TestTruncateDoesNotSplitRune(t *testing.T) {
	in := "abc日本語"
	for n := 1; n <= len(in); n++ {
		got := truncate(in, n)
		if len(got) > n {
			t.Fatalf("truncate(%q, %d) = %d bytes, want <= %d", in, n, len(got), n)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(%q, %d) = %q, not valid UTF-8", in, n, got)
		}
	}
}

func TestTruncateNonPositive(t *testing.T) {
	if got := truncate("abc", 0); got != "" {
		t.Fatalf("truncate(_, 0) = %q, want empty", got)
	}
	if got := truncate("abc", -5); got != "" {
		t.Fatalf("truncate(_, -5) = %q, want empty", got)
	}
}

func TestBuildSystemPromptIsDeterministic(t *testing.T) {
	// An unstable skill list rewrites the prompt on every run, defeating
	// provider prompt caching and making identical queries diverge.
	tools := map[string]Skill{
		"zeta":    {Schema: skills.Skill{Name: "zeta", Description: "z"}},
		"alpha":   {Schema: skills.Skill{Name: "alpha", Description: "a"}},
		"mid":     {Schema: skills.Skill{Name: "mid", Description: "m"}},
		"weather": {Schema: skills.Skill{Name: "weather", Description: "w"}},
	}
	skillNames := map[string]bool{"zeta": true, "alpha": true, "mid": true, "weather": true}

	s := &Service{}
	first := s.buildSystemPrompt(tools, skillNames)
	for i := 0; i < 20; i++ {
		if got := s.buildSystemPrompt(tools, skillNames); got != first {
			t.Fatalf("system prompt changed between builds:\n%s\n---\n%s", first, got)
		}
	}

	ia := strings.Index(first, "- alpha(")
	im := strings.Index(first, "- mid(")
	iz := strings.Index(first, "- zeta(")
	if !(ia < im && im < iz) {
		t.Fatalf("skills not in sorted order (alpha=%d mid=%d zeta=%d):\n%s", ia, im, iz, first)
	}
}

func TestFormatSkillLineSortsParameters(t *testing.T) {
	sk := skills.Skill{
		Name:        "demo",
		Description: "d",
		Parameters: map[string]skills.Param{
			"zulu":  {Required: false},
			"alpha": {Required: true},
			"mike":  {Required: false},
		},
	}
	want := "- demo(alpha (required), mike (optional), zulu (optional)): d"
	for i := 0; i < 20; i++ {
		if got := formatSkillLine(sk); got != want {
			t.Fatalf("formatSkillLine = %q, want %q", got, want)
		}
	}
}

func TestCapBatchEnforcesLimit(t *testing.T) {
	calls := make([]batchCall, 10)
	for i := range calls {
		calls[i] = batchCall{Name: "ping_check"}
	}

	s := &Service{MaxParallel: 3}
	kept, dropped := s.capBatch(calls)
	if len(kept) != 3 || dropped != 7 {
		t.Fatalf("capBatch = %d kept / %d dropped, want 3/7", len(kept), dropped)
	}

	// Unset MaxParallel falls back to the documented default of 4.
	def := &Service{}
	kept, dropped = def.capBatch(calls)
	if len(kept) != 4 || dropped != 6 {
		t.Fatalf("capBatch (default) = %d kept / %d dropped, want 4/6", len(kept), dropped)
	}

	// A batch within the limit is passed through untouched.
	small := calls[:2]
	kept, dropped = s.capBatch(small)
	if len(kept) != 2 || dropped != 0 {
		t.Fatalf("capBatch (small) = %d kept / %d dropped, want 2/0", len(kept), dropped)
	}
}

// RunStream used to emit an "error" event when the model replied with non-JSON,
// while Run recovered with a polite fallback answer for the very same reply —
// so the web UI and Telegram bot showed a raw parser message where the
// non-streaming endpoint answered normally.
func TestRunStreamRecoversFromUnparseableJSON(t *testing.T) {
	svc := newMinimalService(t, &mockLLM{responses: []string{"I am not JSON at all."}})

	out := make(chan Event, 16)
	go svc.RunStream(context.Background(), RunRequest{Query: "hi", MaxSteps: 3}, out)
	evs := collectEvents(out)

	if len(evs) == 0 {
		t.Fatal("no events emitted")
	}
	last := evs[len(evs)-1]
	if last.Event != "final" {
		t.Fatalf("last event = %q, want %q (events: %+v)", last.Event, "final", evs)
	}
	for _, ev := range evs {
		if ev.Event == "error" {
			t.Fatalf("unexpected error event: %+v", ev)
		}
	}
	if last.Answer == "" {
		t.Fatal("fallback final carried no answer")
	}
	if last.Meta["fallback"] != true {
		t.Fatalf("meta.fallback = %v, want true", last.Meta["fallback"])
	}
	if last.SessionID == 0 {
		t.Fatal("fallback final carried no session id — the client cannot continue the conversation")
	}
}

// Run and RunStream must agree on the answer for the same bad model reply.
func TestRunAndRunStreamAgreeOnParseFallback(t *testing.T) {
	bad := "totally not json"

	syncSvc := newMinimalService(t, &mockLLM{responses: []string{bad}})
	res, err := syncSvc.Run(RunRequest{Query: "hi", MaxSteps: 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	streamSvc := newMinimalService(t, &mockLLM{responses: []string{bad}})
	out := make(chan Event, 16)
	go streamSvc.RunStream(context.Background(), RunRequest{Query: "hi", MaxSteps: 3}, out)
	evs := collectEvents(out)
	final := evs[len(evs)-1]

	if final.Answer != res.Answer {
		t.Fatalf("stream answer %q != sync answer %q", final.Answer, res.Answer)
	}
}

// An oversized tool_calls batch must execute at most MaxParallel calls.
func TestRunStreamCapsOversizedBatch(t *testing.T) {
	batch := `{"type":"tool_calls","calls":[
		{"tool":"vector_search","args":{"query":"a"}},
		{"tool":"vector_search","args":{"query":"b"}},
		{"tool":"vector_search","args":{"query":"c"}},
		{"tool":"vector_search","args":{"query":"d"}},
		{"tool":"vector_search","args":{"query":"e"}},
		{"tool":"vector_search","args":{"query":"f"}}
	]}`
	svc := newMinimalService(t, &mockLLM{responses: []string{batch, `{"type":"final","answer":"done"}`}})
	svc.ParallelEnabled = true
	svc.MaxParallel = 2

	out := make(chan Event, 32)
	go svc.RunStream(context.Background(), RunRequest{Query: "hi", MaxSteps: 3}, out)

	calls := 0
	for _, ev := range collectEvents(out) {
		if ev.Event == "tool_call" {
			calls++
		}
	}
	if calls != 2 {
		t.Fatalf("executed %d calls from a 6-call batch, want 2 (MaxParallel)", calls)
	}
}

func TestBatchFollowupReportsDroppedCalls(t *testing.T) {
	results := []batchResult{
		{Call: batchCall{Name: "weather"}, Result: map[string]any{"t": 20}},
	}
	msg := batchFollowup(results, 3)
	if !strings.Contains(msg, "3 further call(s)") || !strings.Contains(msg, "NOT executed") {
		t.Fatalf("followup does not mention the dropped calls: %q", msg)
	}
	if strings.Contains(batchFollowup(results, 0), "NOT executed") {
		t.Fatal("followup must stay quiet when nothing was dropped")
	}
}

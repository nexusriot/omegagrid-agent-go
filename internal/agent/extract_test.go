package agent

import (
	"reflect"
	"testing"
)

func TestParseExtractedFacts(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "envelope object",
			raw:  `{"facts": ["user prefers fra1 region", "project uses Go 1.22"]}`,
			want: []string{"user prefers fra1 region", "project uses Go 1.22"},
		},
		{
			name: "empty envelope",
			raw:  `{"facts": []}`,
			want: []string{},
		},
		{
			name: "bare array",
			raw:  `["fact one"]`,
			want: []string{"fact one"},
		},
		{
			name: "array embedded in noise",
			raw:  "Here are the facts:\n[\"fact one\", \"fact two\"]\nDone.",
			want: []string{"fact one", "fact two"},
		},
		{
			name: "blank entries dropped",
			raw:  `{"facts": ["  ", "real fact", ""]}`,
			want: []string{"real fact"},
		},
		{
			name: "garbage",
			raw:  `not json at all`,
			want: nil,
		},
		{
			name: "object without facts key",
			raw:  `{"type":"final","answer":"hi"}`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseExtractedFacts(tc.raw)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseExtractedFacts(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestMaybeExtractMemoriesGating(t *testing.T) {
	// With the feature disabled, maybeExtractMemories must not call the LLM.
	// The mock returns a canned final answer for any call, so a counter on
	// idx detects unexpected invocations after the synchronous path finishes.
	mock := &mockLLM{responses: []string{`{"facts":["x"]}`}}
	s := &Service{Chat: mock, AutoMemoryExtract: false, AutoMemoryMinAnswerLen: 1}
	s.maybeExtractMemories(1, "query", "a sufficiently long answer")
	if mock.idx != 0 {
		t.Errorf("disabled extraction still called the LLM (idx=%d)", mock.idx)
	}

	// Short answers are skipped even when enabled.
	s.AutoMemoryExtract = true
	s.AutoMemoryMinAnswerLen = 100
	s.maybeExtractMemories(1, "query", "short")
	if mock.idx != 0 {
		t.Errorf("short answer still triggered extraction (idx=%d)", mock.idx)
	}
}

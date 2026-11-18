package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReportUsageShapes(t *testing.T) {
	cases := map[string]Usage{
		`{"usage":{"prompt_tokens":12,"completion_tokens":3}}`:   {12, 3},
		`{"usage":{"input_tokens":40,"output_tokens":9}}`:        {40, 9},
		`{"prompt_eval_count":100,"eval_count":20,"message":{}}`: {100, 20},
		`{"choices":[]}`: {},
		`not json`:       {},
	}
	for body, want := range cases {
		var got Usage
		ctx := WithUsageSink(context.Background(), func(u Usage) { got.PromptTokens += u.PromptTokens; got.CompletionTokens += u.CompletionTokens })
		reportUsage(ctx, []byte(body))
		if got != want {
			t.Errorf("%s: got %+v want %+v", body, got, want)
		}
	}
	reportUsage(context.Background(), []byte(`{"usage":{"prompt_tokens":1}}`)) // no sink: no panic
}

func TestOpenAIReportsUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`))
	}))
	defer srv.Close()
	c := NewOpenAIChat("k", srv.URL, "m", "", "", nil, 5)
	var got Usage
	ctx := WithUsageSink(context.Background(), func(u Usage) { got = u })
	if _, _, err := c.CompleteJSONContext(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if got != (Usage{7, 2}) {
		t.Fatalf("usage %+v", got)
	}
}

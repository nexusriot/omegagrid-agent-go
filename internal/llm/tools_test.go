package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func toolMsgs() []Message {
	return []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "weather", Args: map[string]any{"city": "Berlin"}}}},
		{Role: "tool", ToolCallID: "call_1", Name: "weather", Content: `{"t":15}`},
	}
}

var weatherSpec = []ToolSpec{{Name: "weather", Description: "w", Parameters: map[string]any{"type": "object"}}}

func TestOpenAIChatCompleteTools(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":null,"tool_calls":[
			{"id":"call_9","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}`))
	}))
	defer srv.Close()
	c := NewOpenAIChat("k", srv.URL, "m", "chat_completions", "", nil, 5)
	comp, err := c.CompleteTools(context.Background(), toolMsgs(), weatherSpec)
	if err != nil {
		t.Fatal(err)
	}
	if len(comp.ToolCalls) != 1 || comp.ToolCalls[0].ID != "call_9" || comp.ToolCalls[0].Args["city"] != "Paris" {
		t.Fatalf("completion = %+v", comp)
	}
	if _, ok := body["response_format"]; ok {
		t.Error("response_format json_object sent alongside tools")
	}
	msgs := body["messages"].([]any)
	asst := msgs[2].(map[string]any)
	tc := asst["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "call_1" || tc["function"].(map[string]any)["arguments"] != `{"city":"Berlin"}` {
		t.Fatalf("assistant turn = %v", asst)
	}
	if tool := msgs[3].(map[string]any); tool["role"] != "tool" || tool["tool_call_id"] != "call_1" {
		t.Fatalf("tool turn = %v", tool)
	}
	if fn := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any); fn["name"] != "weather" {
		t.Fatalf("tools = %v", body["tools"])
	}
}

func TestOpenAIResponsesCompleteTools(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"output":[{"type":"function_call","call_id":"fc_1","name":"weather","arguments":"{\"city\":\"Rome\"}"}]}`))
	}))
	defer srv.Close()
	c := NewOpenAIChat("k", srv.URL, "m", "responses", "", nil, 5)
	comp, err := c.CompleteTools(context.Background(), toolMsgs(), weatherSpec)
	if err != nil {
		t.Fatal(err)
	}
	if len(comp.ToolCalls) != 1 || comp.ToolCalls[0].ID != "fc_1" || comp.ToolCalls[0].Args["city"] != "Rome" {
		t.Fatalf("completion = %+v", comp)
	}
	input := body["input"].([]any)
	if fc := input[2].(map[string]any); fc["type"] != "function_call" || fc["call_id"] != "call_1" {
		t.Fatalf("function_call item = %v", fc)
	}
	if out := input[3].(map[string]any); out["type"] != "function_call_output" || out["call_id"] != "call_1" {
		t.Fatalf("function_call_output item = %v", out)
	}
}

func TestOllamaCompleteTools(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"","tool_calls":[
			{"function":{"name":"weather","arguments":{"city":"Oslo"}}},
			{"function":{"name":"weather","arguments":{"city":"Bern"}}}]}}`))
	}))
	defer srv.Close()
	c := NewOllamaChat(srv.URL, "m", 5)
	comp, err := c.CompleteTools(context.Background(), toolMsgs(), weatherSpec)
	if err != nil {
		t.Fatal(err)
	}
	if len(comp.ToolCalls) != 2 || comp.ToolCalls[0].ID == comp.ToolCalls[1].ID || comp.ToolCalls[1].Args["city"] != "Bern" {
		t.Fatalf("completion = %+v (ids must be generated and distinct)", comp)
	}
	if _, ok := body["format"]; ok {
		t.Error("format=json sent alongside tools")
	}
	msgs := body["messages"].([]any)
	if tool := msgs[3].(map[string]any); tool["tool_name"] != "weather" {
		t.Fatalf("tool turn = %v", tool)
	}
}

func TestToolsUnsupportedIsRecognised(t *testing.T) {
	for _, reply := range []string{
		`{"error":"registry.ollama.ai/library/llama3:latest does not support tools"}`,
		`{"error":{"message":"tools is not supported for this model"}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(reply))
		}))
		_, errO := NewOllamaChat(srv.URL, "m", 5).CompleteTools(context.Background(), toolMsgs(), weatherSpec)
		_, errA := NewOpenAIChat("k", srv.URL, "m", "chat_completions", "", nil, 5).CompleteTools(context.Background(), toolMsgs(), weatherSpec)
		srv.Close()
		if !errors.Is(errO, ErrToolsUnsupported) || !errors.Is(errA, ErrToolsUnsupported) {
			t.Errorf("%s: ollama=%v openai=%v, want ErrToolsUnsupported", reply, errO, errA)
		}
	}
	// An unrelated 400 is not mistaken for it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"context length exceeded"}`))
	}))
	defer srv.Close()
	if _, err := NewOllamaChat(srv.URL, "m", 5).CompleteTools(context.Background(), toolMsgs(), weatherSpec); errors.Is(err, ErrToolsUnsupported) {
		t.Fatal("context-length error classified as tools-unsupported")
	}
}

func TestDecodeArgs(t *testing.T) {
	if got := decodeArgs(`{"a":1}`); got["a"] != float64(1) {
		t.Fatal(got)
	}
	if got := decodeArgs(`not json`); !strings.Contains(got["_raw_arguments"].(string), "not json") {
		t.Fatal(got)
	}
	if got := decodeArgs(nil); len(got) != 0 {
		t.Fatal(got)
	}
}

package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type embeddingsClient interface {
	embed(text string) ([]float32, error)
}

type ollamaEmbeddings struct {
	url   string
	model string
	http  *http.Client
}

func newOllamaEmbeddings(baseURL, model string, timeoutSec float64) *ollamaEmbeddings {
	return &ollamaEmbeddings{
		url:   strings.TrimRight(baseURL, "/"),
		model: model,
		http:  &http.Client{Timeout: time.Duration(timeoutSec * float64(time.Second))},
	}
}

// Three fallback endpoints, mirroring Python sidecar order.
var ollamaEndpoints = []string{"/api/embed", "/v1/embeddings", "/api/embeddings"}

func (o *ollamaEmbeddings) embed(text string) ([]float32, error) {
	var lastErr error
	for _, ep := range ollamaEndpoints {
		v, err := o.tryEndpoint(ep, text)
		if err == nil {
			return v, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (o *ollamaEmbeddings) tryEndpoint(ep, text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{
		"model":  o.model,
		"input":  text,
		"prompt": text, // legacy /api/embeddings field
	})
	resp, err := o.http.Post(o.url+ep, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama %s: %d", ep, resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	// /api/embed → {"embeddings":[[...]]}
	if embs, ok := m["embeddings"].([]any); ok && len(embs) > 0 {
		return anyToFloat32s(embs[0])
	}
	// /v1/embeddings → {"data":[{"embedding":[...]}]}
	if data, ok := m["data"].([]any); ok && len(data) > 0 {
		if item, ok := data[0].(map[string]any); ok {
			return anyToFloat32s(item["embedding"])
		}
	}
	// /api/embeddings → {"embedding":[...]}
	if emb, ok := m["embedding"]; ok {
		return anyToFloat32s(emb)
	}
	return nil, fmt.Errorf("could not parse embedding from ollama %s", ep)
}

type openAIEmbeddings struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func newOpenAIEmbeddings(baseURL, apiKey, model string, timeoutSec float64) *openAIEmbeddings {
	return &openAIEmbeddings{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: time.Duration(timeoutSec * float64(time.Second))},
	}
}

func (o *openAIEmbeddings) embed(text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": o.model, "input": text})
	req, _ := http.NewRequest(http.MethodPost, o.baseURL+"/embeddings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("openai embeddings: %d %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("openai embeddings: empty data")
	}
	return float64sToFloat32s(out.Data[0].Embedding), nil
}

func anyToFloat32s(v any) ([]float32, error) {
	s, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected []any, got %T", v)
	}
	out := make([]float32, len(s))
	for i, x := range s {
		f, ok := x.(float64)
		if !ok {
			return nil, fmt.Errorf("embedding[%d]: expected float64, got %T", i, x)
		}
		out[i] = float32(f)
	}
	return out, nil
}

func float64sToFloat32s(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}

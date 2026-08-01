package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nexusriot/omegagrid-agent-go/internal/config"
)

// endpointRecorder tracks which Ollama endpoints were tried, in order.
type endpointRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (e *endpointRecorder) add(p string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.paths = append(e.paths, p)
}

func (e *endpointRecorder) seen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.paths...)
}

// Ollama's embedding API changed shape across versions, so the client tries
// three endpoints in order. Each one answers with a different JSON envelope.
func TestOllamaEmbeddingsEnvelopeShapes(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		body     string
	}{
		{"api_embed", "/api/embed", `{"embeddings":[[0.1,0.2,0.3]]}`},
		{"v1_embeddings", "/v1/embeddings", `{"data":[{"embedding":[0.1,0.2,0.3]}]}`},
		{"api_embeddings_legacy", "/api/embeddings", `{"embedding":[0.1,0.2,0.3]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &endpointRecorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.add(r.URL.Path)
				if r.URL.Path != c.endpoint {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()

			got, err := newOllamaEmbeddings(srv.URL, "nomic-embed-text", 5).embed("hello")
			if err != nil {
				t.Fatalf("embed: %v", err)
			}
			want := []float32{0.1, 0.2, 0.3}
			if len(got) != len(want) {
				t.Fatalf("embedding len = %d, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("embedding[%d] = %v, want %v", i, got[i], want[i])
				}
			}
		})
	}
}

func TestOllamaEmbeddingsTriesEndpointsInOrder(t *testing.T) {
	rec := &endpointRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r.URL.Path)
		// Only the last fallback works.
		if r.URL.Path == "/api/embeddings" {
			_, _ = w.Write([]byte(`{"embedding":[1]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := newOllamaEmbeddings(srv.URL, "m", 5).embed("x"); err != nil {
		t.Fatalf("embed: %v", err)
	}
	want := []string{"/api/embed", "/v1/embeddings", "/api/embeddings"}
	got := rec.seen()
	if len(got) != len(want) {
		t.Fatalf("tried %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attempt %d hit %q, want %q", i, got[i], want[i])
		}
	}
}

// The first working endpoint wins — no pointless extra requests.
func TestOllamaEmbeddingsStopsAtFirstSuccess(t *testing.T) {
	rec := &endpointRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r.URL.Path)
		_, _ = w.Write([]byte(`{"embeddings":[[1,2]]}`))
	}))
	defer srv.Close()

	if _, err := newOllamaEmbeddings(srv.URL, "m", 5).embed("x"); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if got := rec.seen(); len(got) != 1 {
		t.Fatalf("made %d requests, want 1: %v", len(got), got)
	}
}

func TestOllamaEmbeddingsPayload(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"embeddings":[[1]]}`))
	}))
	defer srv.Close()

	if _, err := newOllamaEmbeddings(srv.URL+"/", "nomic-embed-text", 5).embed("hello"); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if payload["model"] != "nomic-embed-text" {
		t.Fatalf("model = %v", payload["model"])
	}
	// Both field names are sent because the three endpoints disagree on which
	// one carries the text.
	if payload["input"] != "hello" || payload["prompt"] != "hello" {
		t.Fatalf("payload = %v, want both input and prompt set", payload)
	}
}

func TestOllamaEmbeddingsAllEndpointsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newOllamaEmbeddings(srv.URL, "m", 5).embed("x")
	if err == nil {
		t.Fatal("expected an error when every endpoint fails")
	}
	// The last endpoint's status is what gets surfaced in /health.
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error lost the status: %v", err)
	}
}

func TestOllamaEmbeddingsUnparseableResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Valid JSON, but no recognised embedding field.
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	_, err := newOllamaEmbeddings(srv.URL, "m", 5).embed("x")
	if err == nil {
		t.Fatal("expected an error for a response with no embedding")
	}
	if !strings.Contains(err.Error(), "could not parse embedding") {
		t.Fatalf("error = %v", err)
	}
}

func TestOllamaEmbeddingsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	if _, err := newOllamaEmbeddings(url, "m", 5).embed("x"); err == nil {
		t.Fatal("expected a transport error")
	}
}

func TestOpenAIEmbeddings(t *testing.T) {
	var payload map[string]any
	var auth, path, ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		ct = r.Header.Get("Content-Type")
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.5,-0.25]}]}`))
	}))
	defer srv.Close()

	got, err := newOpenAIEmbeddings(srv.URL+"/", "sk-test", "text-embedding-3-small", 5).embed("hello")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(got) != 2 || got[0] != 0.5 || got[1] != -0.25 {
		t.Fatalf("embedding = %v", got)
	}
	if path != "/embeddings" {
		t.Fatalf("path = %q, want /embeddings", path)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", auth)
	}
	if ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if payload["model"] != "text-embedding-3-small" || payload["input"] != "hello" {
		t.Fatalf("payload = %v", payload)
	}
}

func TestOpenAIEmbeddingsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided"}}`))
	}))
	defer srv.Close()

	_, err := newOpenAIEmbeddings(srv.URL, "bad", "m", 5).embed("x")
	if err == nil {
		t.Fatal("expected an error for HTTP 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Incorrect API key") {
		t.Fatalf("error lost the server's explanation: %v", err)
	}
}

func TestOpenAIEmbeddingsEmptyData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	_, err := newOpenAIEmbeddings(srv.URL, "k", "m", 5).embed("x")
	if err == nil {
		t.Fatal("expected an error for an empty data array")
	}
	if !strings.Contains(err.Error(), "empty data") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAIEmbeddingsDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>proxy error</html>`))
	}))
	defer srv.Close()

	if _, err := newOpenAIEmbeddings(srv.URL, "k", "m", 5).embed("x"); err == nil {
		t.Fatal("expected a decode error")
	}
}

// A malformed base URL must surface as an error, not a nil-request panic.
func TestOpenAIEmbeddingsMalformedBaseURL(t *testing.T) {
	if _, err := newOpenAIEmbeddings("http://bad host/v1", "k", "m", 5).embed("x"); err == nil {
		t.Fatal("expected an error for an unparseable base URL")
	}
}

// buildEmbeddings picks the backend from LLM_PROVIDER and refuses to build a
// keyless cloud client — otherwise every embed call would fail at runtime with
// a confusing 401 instead of a clear startup error.
func TestBuildEmbeddingsProviderSelection(t *testing.T) {
	if _, err := buildEmbeddings(config.Config{Provider: "openai"}); err == nil {
		t.Fatal("expected an error for openai without a key")
	}
	if _, err := buildEmbeddings(config.Config{Provider: "digitalocean"}); err == nil {
		t.Fatal("expected an error for digitalocean without a key")
	}

	cases := []struct {
		cfg  config.Config
		want any
	}{
		{config.Config{Provider: "ollama", OllamaURL: "http://x", OllamaEmbedModel: "nomic"}, &ollamaEmbeddings{}},
		{config.Config{Provider: "", OllamaURL: "http://x"}, &ollamaEmbeddings{}},
		{config.Config{Provider: "OpenAI", OpenAIAPIKey: "k"}, &openAIEmbeddings{}},
		{config.Config{Provider: "openai-codex", OpenAIAPIKey: "k"}, &openAIEmbeddings{}},
		{config.Config{Provider: "codex", OpenAIAPIKey: "k"}, &openAIEmbeddings{}},
		{config.Config{Provider: "do", DigitalOceanAPIKey: "k"}, &openAIEmbeddings{}},
		{config.Config{Provider: "digitalocean", DigitalOceanAPIKey: "k"}, &openAIEmbeddings{}},
	}
	for _, c := range cases {
		got, err := buildEmbeddings(c.cfg)
		if err != nil {
			t.Fatalf("buildEmbeddings(%q): %v", c.cfg.Provider, err)
		}
		switch c.want.(type) {
		case *ollamaEmbeddings:
			if _, ok := got.(*ollamaEmbeddings); !ok {
				t.Fatalf("provider %q gave %T, want *ollamaEmbeddings", c.cfg.Provider, got)
			}
		case *openAIEmbeddings:
			if _, ok := got.(*openAIEmbeddings); !ok {
				t.Fatalf("provider %q gave %T, want *openAIEmbeddings", c.cfg.Provider, got)
			}
		}
	}
}

// DigitalOcean reuses the OpenAI client but must use its own key, URL and model.
func TestBuildEmbeddingsDigitalOceanUsesOwnSettings(t *testing.T) {
	got, err := buildEmbeddings(config.Config{
		Provider:               "digitalocean",
		DigitalOceanAPIKey:     "do-key",
		DigitalOceanBaseURL:    "https://inference.do-ai.run/v1",
		DigitalOceanEmbedModel: "qwen3-embedding-0.6b",
		OpenAIAPIKey:           "sk-should-not-be-used",
		OpenAIEmbedModel:       "text-embedding-3-small",
	})
	if err != nil {
		t.Fatalf("buildEmbeddings: %v", err)
	}
	oa, ok := got.(*openAIEmbeddings)
	if !ok {
		t.Fatalf("got %T", got)
	}
	if oa.apiKey != "do-key" {
		t.Fatalf("apiKey = %q, want the DigitalOcean key", oa.apiKey)
	}
	if oa.model != "qwen3-embedding-0.6b" {
		t.Fatalf("model = %q, want the DigitalOcean model", oa.model)
	}
	if oa.baseURL != "https://inference.do-ai.run/v1" {
		t.Fatalf("baseURL = %q", oa.baseURL)
	}
}

func TestAnyToFloat32sRejectsBadShapes(t *testing.T) {
	if _, err := anyToFloat32s("not an array"); err == nil {
		t.Fatal("expected an error for a non-array")
	}
	if _, err := anyToFloat32s([]any{1.0, "two"}); err == nil {
		t.Fatal("expected an error for a non-numeric element")
	}
	got, err := anyToFloat32s([]any{1.5, -2.5, 0.0})
	if err != nil {
		t.Fatalf("anyToFloat32s: %v", err)
	}
	if len(got) != 3 || got[0] != 1.5 || got[1] != -2.5 {
		t.Fatalf("got %v", got)
	}
}

func TestFloat64sToFloat32s(t *testing.T) {
	got := float64sToFloat32s([]float64{0.5, -1.25})
	if len(got) != 2 || got[0] != 0.5 || got[1] != -1.25 {
		t.Fatalf("got %v", got)
	}
	if got := float64sToFloat32s(nil); len(got) != 0 {
		t.Fatalf("nil input gave %v", got)
	}
}

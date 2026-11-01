// Package httpapi wires together the chi router and request handlers for the
// gateway service.  Routes mirror the original FastAPI gateway 1:1 so existing
// clients (frontend, telegram bot, curl scripts, ...) keep working.
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/mcp"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
	"github.com/nexusriot/omegagrid-agent-go/internal/version"
)

// Deps bundles everything a request handler may need.  Constructed once at
// startup by cmd/gateway/main.go.
type Deps struct {
	Cfg       config.Config
	Agent     *agent.Service
	Memory    *memory.Client
	Skills    *skills.Client
	Chat      llm.ChatClient
	Scheduler *scheduler.Store
	// Runner executes run-now requests; nil disables that route (503).
	Runner *scheduler.Runner

	// ToolProvider backs the MCP server endpoint. When nil or disabled by
	// config, the /mcp route is not mounted.
	ToolProvider mcp.ToolProvider
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(hostGuard(d.Cfg.APIToken, d.Cfg.AllowedHosts))
	r.Use(corsMiddleware(d.Cfg.CORSAllowedOrigins))
	r.Use(originGuard(d.Cfg.CORSAllowedOrigins))
	r.Use(limitBody(maxRequestBodyBytes))

	r.Get("/health", d.handleHealth)

	if d.Cfg.MCPServerEnabled && d.ToolProvider != nil {
		srv := mcp.NewServer(d.ToolProvider, "omegagrid-agent-go", version.Version)
		r.With(requireToken(d.Cfg.APIToken)).Handle("/mcp", srv.Handler())
	}

	r.Route("/api", func(r chi.Router) {
		r.Use(requireToken(d.Cfg.APIToken))
		r.Post("/query", d.handleQuery)
		r.Post("/query/stream", d.handleQueryStream)

		r.Post("/sessions/new", d.handleNewSession)
		r.Get("/sessions", d.handleListSessions)
		r.Get("/sessions/{sid}/messages", d.handleSessionMessages)
		r.Patch("/sessions/{sid}", d.handleRenameSession)
		r.Delete("/sessions/{sid}", d.handleDeleteSession)
		r.Get("/stats/usage", d.handleUsage)

		r.Post("/memory/add", d.handleMemoryAdd)
		r.Post("/memory/search", d.handleMemorySearch)
		r.Get("/memory", d.handleMemoryList)
		r.Get("/memory/namespaces", d.handleMemoryNamespaces)
		r.Get("/memory/export", d.handleMemoryExport)
		r.Post("/memory/import", d.handleMemoryImport)
		r.Get("/memory/{id}", d.handleMemoryGet)
		r.Put("/memory/{id}", d.handleMemoryUpdate)
		r.Delete("/memory/{id}", d.handleMemoryDelete)

		r.Get("/skills", d.handleListSkills)
		r.Post("/skills/validate", d.handleSkillValidate)
		r.Post("/skills/source", d.handleSkillSave)
		r.Get("/skills/{name}/source", d.handleSkillSource)
		r.Delete("/skills/{name}", d.handleSkillDelete)
		r.Post("/skills/{name}/invoke", d.handleSkillInvoke)
		r.Get("/tools", d.handleListTools)

		r.Get("/invocations", d.handleListInvocations)
		r.Get("/invocations/{id}", d.handleGetInvocation)
		r.Post("/invocations/{id}/replay", d.handleReplayInvocation)

		r.Post("/scheduler/tasks", d.handleSchedulerCreate)
		r.Get("/scheduler/tasks", d.handleSchedulerList)
		r.Get("/scheduler/tasks/{id}", d.handleSchedulerGet)
		r.Post("/scheduler/tasks/{id}/enable", d.handleSchedulerEnable)
		r.Post("/scheduler/tasks/{id}/disable", d.handleSchedulerDisable)
		r.Delete("/scheduler/tasks/{id}", d.handleSchedulerDelete)
		r.Post("/scheduler/tasks/{id}/run", d.handleSchedulerRun)
		r.Get("/scheduler/tasks/{id}/runs", d.handleSchedulerRuns)
	})

	return r
}

// maxRequestBodyBytes caps every request body. The largest legitimate
// payloads (a memory entry, a skill invocation) are a few KB; without a cap a
// single request could make the JSON decoder buffer gigabytes.
const maxRequestBodyBytes = 4 << 20

func limitBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The import route applies its own, larger cap (maxImportBytes);
			// a MaxBytesReader cannot be widened once wrapped.
			if r.Body != nil && r.URL.Path != "/api/memory/import" {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// corsMiddleware answers cross-origin requests only for the configured
// origins. The bundled UI is served same-origin through nginx and needs no
// CORS at all; the previous wildcard let any website the operator visited
// read API responses from a gateway on localhost.
func corsMiddleware(allowed []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && originAllowed(origin, allowed) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id")
				w.Header().Add("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// originGuard rejects state-changing requests that a browser sent from a
// foreign origin. A cross-site form or text/plain fetch is a "simple" request
// that skips the CORS preflight, so withholding CORS headers alone does not
// stop it from executing — only from reading the response. Clients that are
// not browsers (curl, the CLI, the Telegram bot) send no Origin and pass.
func originGuard(allowed []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, r.Host) {
				next.ServeHTTP(w, r)
				return
			}
			if originAllowed(origin, allowed) {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
		})
	}
}

// hostGuard defends a token-less gateway against DNS rebinding. A page on
// evil.example can re-point its own name at 127.0.0.1; the browser then sends
// Host: evil.example and Origin: http://evil.example, which originGuard
// rightly treats as same-origin, and the page gets full read/write access to
// the API (and through /api/query to shell_command). Such a request always
// carries the attacker's dotted DNS name, so while no token is configured
// only Host values a public resolver cannot hand out are accepted: IP
// literals, single-label names (localhost, the compose service name
// "gateway") and *.localhost, plus whatever GATEWAY_ALLOWED_HOSTS lists. A
// token makes the check unnecessary: the rebinding page cannot read it.
func hostGuard(token string, allowed []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token != "" {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hostTrusted(r.Host, allowed) {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusForbidden,
				"untrusted Host header; set GATEWAY_API_TOKEN or add the host to GATEWAY_ALLOWED_HOSTS")
		})
	}
}

func hostTrusted(hostport string, allowed []string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" {
		return true
	}
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") {
		return true
	}
	for _, a := range allowed {
		if a == "*" || strings.EqualFold(strings.TrimSuffix(a, "."), host) {
			return true
		}
	}
	return false
}

func originAllowed(origin string, allowed []string) bool {
	return slices.Contains(allowed, "*") || slices.Contains(allowed, strings.TrimRight(origin, "/"))
}

// requireToken enforces "Authorization: Bearer <token>" when token is set.
// An empty token leaves the API open, which is the historical behaviour and
// is only safe while the gateway listens on loopback.
func requireToken(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if token == "" {
			return next
		}
		want := []byte("Bearer " + token)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := []byte(r.Header.Get("Authorization"))
			if subtle.ConstantTimeCompare(got, want) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="omegagrid"`)
				writeError(w, http.StatusUnauthorized, "missing or invalid API token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// queryInt reads a numeric query parameter, falling back to def when it is
// absent or unparseable. A garbage ?limit= is a client mistake, not a reason to
// fail the request.
func queryInt(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func queryFloat(s string, def float64) float64 {
	if s == "" {
		return def
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return def
}

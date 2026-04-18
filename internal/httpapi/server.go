// Package httpapi wires together the chi router and request handlers for the
// gateway service.  Routes mirror the original FastAPI gateway 1:1 so existing
// clients (frontend, telegram bot, curl scripts, ...) keep working.
package httpapi

import (
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/nexusriot/omegagrid-agent-go/internal/agent"
	"github.com/nexusriot/omegagrid-agent-go/internal/config"
	"github.com/nexusriot/omegagrid-agent-go/internal/llm"
	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
	"github.com/nexusriot/omegagrid-agent-go/internal/scheduler"
	"github.com/nexusriot/omegagrid-agent-go/internal/skills"
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
	// WebUI is the compiled React app embedded FS (web/dist).
	// When nil the /ui/* route is omitted.
	WebUI fs.FS
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(corsMiddleware) // permissive CORS, matches the FastAPI default

	r.Get("/health", d.handleHealth)

	r.Route("/api", func(r chi.Router) {
		r.Post("/query", d.handleQuery)
		r.Post("/query/stream", d.handleQueryStream)

		r.Post("/sessions/new", d.handleNewSession)
		r.Get("/sessions", d.handleListSessions)
		r.Get("/sessions/{sid}/messages", d.handleSessionMessages)

		r.Post("/memory/add", d.handleMemoryAdd)
		r.Post("/memory/search", d.handleMemorySearch)

		r.Get("/skills", d.handleListSkills)
		r.Get("/tools", d.handleListTools)

		r.Post("/scheduler/tasks", d.handleSchedulerCreate)
		r.Get("/scheduler/tasks", d.handleSchedulerList)
		r.Get("/scheduler/tasks/{id}", d.handleSchedulerGet)
		r.Post("/scheduler/tasks/{id}/enable", d.handleSchedulerEnable)
		r.Post("/scheduler/tasks/{id}/disable", d.handleSchedulerDisable)
		r.Delete("/scheduler/tasks/{id}", d.handleSchedulerDelete)
	})

	// Serve the compiled React UI at /ui/*.
	// Redirect bare / to /ui/ for convenience.
	if d.WebUI != nil {
		fileServer := http.FileServer(http.FS(d.WebUI))
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
		r.Handle("/ui", http.RedirectHandler("/ui/", http.StatusMovedPermanently))
		r.Handle("/ui/*", http.StripPrefix("/ui", fileServer))
	}

	return r
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/giantbeaver9/pi-home-server-page/backend/internal/db"
	"github.com/giantbeaver9/pi-home-server-page/backend/internal/llm"
)

// API holds shared dependencies for the HTTP handlers.
type API struct {
	store      *db.Store
	llmBaseURL string
	// agent drives the tool-calling assistant. Nil when the LLM is unconfigured.
	agent *llm.Agent
	// appPassword gates the API behind a single shared password. Empty disables
	// auth entirely (VPN-only mode) and withAuth becomes a pass-through.
	appPassword string
}

// Register wires all /api/* routes onto mux. Go 1.22+ method+pattern routing
// means these take precedence over the SPA catch-all registered at "/".
// llmBaseURL is the OpenAI-compatible local LLM base URL (empty = LLM disabled).
// appPassword enables single-password cookie auth (empty = VPN-only, no auth).
// llmModel is passed to the tool-calling agent (empty = server's loaded model).
func Register(mux *http.ServeMux, store *db.Store, llmBaseURL, appPassword, llmModel string) {
	a := &API{store: store, llmBaseURL: llmBaseURL, appPassword: appPassword}
	// The agentic assistant is only available when an LLM is configured; the raw
	// /api/llm proxy above stays usable regardless.
	if llmBaseURL != "" {
		a.agent = llm.NewAgent(llmBaseURL, llmModel, store)
	}

	// Public routes: health (for uptime monitors) and the auth endpoints.
	mux.HandleFunc("GET /api/health", a.health)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.HandleFunc("GET /api/auth/me", a.authMe)

	mux.HandleFunc("GET /api/stats", a.withAuth(a.getStats))

	mux.HandleFunc("GET /api/llm/status", a.withAuth(a.llmStatus))
	mux.HandleFunc("POST /api/llm/chat/completions", a.withAuth(a.llmChat))

	mux.HandleFunc("POST /api/assistant/chat", a.withAuth(a.assistantChat))

	mux.HandleFunc("GET /api/tiles", a.withAuth(a.listTiles))
	mux.HandleFunc("POST /api/tiles", a.withAuth(a.createTile))
	mux.HandleFunc("PUT /api/tiles/{id}", a.withAuth(a.updateTile))
	mux.HandleFunc("DELETE /api/tiles/{id}", a.withAuth(a.deleteTile))

	mux.HandleFunc("GET /api/notes", a.withAuth(a.listNotes))
	mux.HandleFunc("POST /api/notes", a.withAuth(a.createNote))
	mux.HandleFunc("PUT /api/notes/{id}", a.withAuth(a.updateNote))
	mux.HandleFunc("DELETE /api/notes/{id}", a.withAuth(a.deleteNote))

	mux.HandleFunc("GET /api/todos", a.withAuth(a.listTodos))
	mux.HandleFunc("POST /api/todos", a.withAuth(a.createTodo))
	mux.HandleFunc("PUT /api/todos/{id}", a.withAuth(a.updateTodo))
	mux.HandleFunc("DELETE /api/todos/{id}", a.withAuth(a.deleteTodo))

	mux.HandleFunc("GET /api/events", a.withAuth(a.listEvents))
	mux.HandleFunc("POST /api/events", a.withAuth(a.createEvent))
	mux.HandleFunc("PUT /api/events/{id}", a.withAuth(a.updateEvent))
	mux.HandleFunc("DELETE /api/events/{id}", a.withAuth(a.deleteEvent))
}

// withAuth is the authentication seam and the single choke point every gated
// API route passes through. With no app password set access is gated by the VPN
// tunnel, so it is a pass-through; otherwise it requires a valid signed session
// cookie and slides its expiry on each request.
func (a *API) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.appPassword == "" {
			next(w, r)
			return
		}
		if !a.sessionValid(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		a.issueSession(w, r) // sliding expiry
		next(w, r)
	}
}

// Logging is a lightweight request logger wrapping the whole mux.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		badRequest(w, "invalid json: "+err.Error())
		return false
	}
	return true
}

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return 0, false
	}
	return id, true
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

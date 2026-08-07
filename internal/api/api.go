// Package api holds the HTTP layer: the App composition root (app.go) that
// registers every REST route + the SSE stream, plus the shared JSON helpers and
// a request logger. Ported from the pi-server stdlib net/http scaffold (Go 1.22
// method-pattern ServeMux).
package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"
)

// Logging is a lightweight request logger wrapping the whole mux.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

// --- shared helpers (exported for feature handler packages) ---

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func ReadJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		BadRequest(w, "invalid json: "+err.Error())
		return false
	}
	return true
}

func ParseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		BadRequest(w, "invalid id")
		return 0, false
	}
	return id, true
}

func BadRequest(w http.ResponseWriter, msg string) {
	WriteJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

func ServerError(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

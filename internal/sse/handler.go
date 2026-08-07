package sse

import (
	"context"
	"net/http"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler serves the SSE stream: on connect it ParseLastEventID → ReplaySince →
// writes+flushes each event, then joins the live tail (LISTEN/NOTIFY fanned out
// through the hub) until the request context is done.
//
// One LISTEN connection is shared by all connected clients: the first client to
// arrive starts the listener goroutine, the last to leave stops it (cancelling
// its context releases the pooled connection, so pool.Close never blocks on a
// checked-out connection).
type Handler struct {
	pool *pgxpool.Pool
	hub  *hub

	mu      sync.Mutex
	nactive int
	cancel  context.CancelFunc
}

// NewHandler builds a Handler over the pool.
func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool, hub: newHub()}
}

// join subscribes a client and starts the shared listener if it is the first.
func (h *Handler) join() chan Event {
	ch := h.hub.subscribe()
	h.mu.Lock()
	h.nactive++
	if h.nactive == 1 {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		go h.listen(ctx)
	}
	h.mu.Unlock()
	return ch
}

// leave unsubscribes a client and stops the shared listener if it is the last.
func (h *Handler) leave(ch chan Event) {
	h.hub.unsubscribe(ch)
	h.mu.Lock()
	h.nactive--
	if h.nactive <= 0 {
		h.nactive = 0
		if h.cancel != nil {
			h.cancel()
			h.cancel = nil
		}
	}
	h.mu.Unlock()
}

// listen owns the single LISTEN connection: each NOTIFY carries a message id,
// which it hydrates into an Event and hands to the hub for fan-out. It returns
// (releasing the connection) as soon as ctx is cancelled — i.e. the last client
// disconnected — or the connection breaks.
func (h *Handler) listen(ctx context.Context) {
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `LISTEN "`+Channel+`"`); err != nil {
		return
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return // ctx cancelled or connection lost
		}
		id, err := strconv.ParseInt(n.Payload, 10, 64)
		if err != nil {
			continue // ignore malformed payloads
		}
		ev, err := loadEvent(ctx, h.pool, id)
		if err != nil {
			continue // row not visible yet / gone — reconnect replay covers it
		}
		h.hub.broadcast(ev)
	}
}

// ServeHTTP serves the SSE stream for one client.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()

	// Subscribe to the live tail BEFORE replaying, so an event committed during
	// replay is buffered on our channel rather than lost between the two phases.
	ch := h.join()
	defer h.leave(ch)

	// Connect-time replay of the durable log past the client's cursor.
	last := ParseLastEventID(r)
	replayed, err := ReplaySince(ctx, h.pool, last)
	if err == nil {
		for _, ev := range replayed {
			if _, err := w.Write(ev.SSE()); err != nil {
				return
			}
			last = ev.ID
		}
		flusher.Flush()
	}

	// Live tail until the client goes away.
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			// Skip anything already covered by replay (dedupe the overlap).
			if ev.ID <= last {
				continue
			}
			if _, err := w.Write(ev.SSE()); err != nil {
				return
			}
			last = ev.ID
			flusher.Flush()
		}
	}
}

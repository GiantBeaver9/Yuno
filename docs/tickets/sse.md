# sse — live monitor: Publisher, LISTEN/NOTIFY tail, Last-Event-ID replay

## Unit
sse

## Package / Owned files
`internal/sse/*.go`

## Deps
store

## Tier
hard

## Interfaces
```go
package sse

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/giantbeaver9/yuno/internal/store"
)

// Channel is the Postgres NOTIFY channel name.
const Channel = "yuno_messages"

// Publisher is the one-method seam (ADR-23). NOTIFY is the default impl;
// Redis/NATS is a later drop-in, no rewrite.
type Publisher interface {
	Publish(ctx context.Context, messageID int64) error
}

// NotifyPublisher emits `SELECT pg_notify(Channel, <id>)`. Emit it in the SAME
// transaction as the message insert so the tail can never diverge from the row.
type NotifyPublisher struct{ /* pool *pgxpool.Pool */ }

func NewNotifyPublisher(pool *pgxpool.Pool) *NotifyPublisher
func (p *NotifyPublisher) Publish(ctx context.Context, messageID int64) error

// Event is a projection of a `message` row for the stream (kept local so this
// package depends only on store, not bus).
type Event struct {
	ID        int64  // message.id — the SSE event id / cursor
	RunID     string
	Seq       int64
	FromRef   string
	ToRef     string
	Content   string
	Decision  string
	Summary   string
	Status    string
	CreatedAt time.Time
}

// SSE renders the wire frame: "id: <ID>\ndata: <json(Event)>\n\n".
func (e Event) SSE() []byte

// ReplaySince returns message rows with id > lastID ordered by id — the
// reconnect replay (testable core).
func ReplaySince(ctx context.Context, q store.Querier, lastID int64) ([]Event, error)

// ParseLastEventID reads the Last-Event-ID header (falling back to a query
// param); non-numeric/absent → 0.
func ParseLastEventID(r *http.Request) int64

// Handler serves the SSE stream: on connect it ParseLastEventID → ReplaySince →
// writes+flushes each event, then LISTENs on Channel and tails new rows (loading
// each by id) with per-client fan-out until the request context is done.
type Handler struct{ /* pool *pgxpool.Pool */ }

func NewHandler(pool *pgxpool.Pool) *Handler
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

## Accept
- `ReplaySince(ctx,q,last)` returns exactly the message rows with `id > last`, ordered ascending by `id`.
- `Event.SSE()` emits a valid SSE frame beginning `id: ` and terminated by a blank line, with a JSON `data:` payload.
- `ParseLastEventID` yields the numeric header value, or `0` when the header is absent or non-numeric.
- `Handler` sets `Content-Type: text/event-stream`, replays `id>Last-Event-ID`, then tails via LISTEN/NOTIFY; a reconnect with `Last-Event-ID` produces a gap-free continuation.
- `NotifyPublisher.Publish` sends the id over `Channel` (≤8KB payload — id only, hydrate from the row).

## Test cases
All DB tests use `testutil.NewDB`. Seeding `message` rows requires a `run` row (message.run_id → run.guid); insert a run first.
- **[positive]** Seed 5 message rows (ids 1..5); `ReplaySince(0)` returns all 5 in id order; `ReplaySince(3)` returns ids 4,5 only.
- **[negative]** `ReplaySince(lastID)` with `lastID ≥ max(id)` → empty slice, nil error; `ParseLastEventID` on a request with `Last-Event-ID: abc` → `0`.
- **[edge]** `ReplaySince(0)` on an empty table → empty slice; `Event.SSE()` for a seeded event starts with `"id: "` and ends with the blank-line terminator (`\n\n`).

## Salvage
Net-new (§14; PORT.md "Net-new" #2). No SSE/WebSocket/pub-sub anywhere in salvage — `api/events.go` is a false friend (calendar CRUD). LISTEN/NOTIFY and the replay cursor are built fresh.

// Package sse serves the live run monitor as a Server-Sent Events stream.
//
// Three layers cooperate so a browser can reconnect without losing a beat:
//
//  1. Durable log — every bus message is a `message` row whose IDENTITY column
//     `id` is a global, monotonic cursor. That id is the SSE event id.
//  2. LISTEN/NOTIFY tail — a write publishes its new id on the Postgres
//     Channel (see NotifyPublisher); the Handler holds one LISTEN connection
//     and fans each id out to every connected client through an in-memory hub.
//  3. Last-Event-ID replay — on (re)connect the Handler reads the browser's
//     Last-Event-ID and replays the durable rows with id greater than it
//     (ReplaySince) before joining the live tail, so the stream is gap-free.
//
// The Publisher interface is the seam (ADR-23): NOTIFY is the default backend,
// but a Redis/NATS publisher can drop in without touching the Handler. The
// package depends only on store — Event is a local projection of a message row,
// not an import of bus, keeping sse a store-only leaf.
package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/giantbeaver9/yuno/internal/store"
)

// Channel is the Postgres NOTIFY channel name.
const Channel = "yuno_messages"

// Event is a projection of a `message` row for the stream (kept local so this
// package depends only on store, not bus).
type Event struct {
	ID        int64 // message.id — the SSE event id / cursor
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
func (e Event) SSE() []byte {
	data, err := json.Marshal(e)
	if err != nil {
		// Event has no un-marshalable fields; degrade to an empty object
		// rather than emit a malformed frame.
		data = []byte("{}")
	}
	return []byte(fmt.Sprintf("id: %d\ndata: %s\n\n", e.ID, data))
}

// replayColumns are the message columns projected into an Event, in scan order.
const replayColumns = `id, run_id, seq, from_ref, to_ref, content, decision, summary, status, created_at`

// scanEvent reads one Event from a row (pgx.Row or pgx.Rows both satisfy Scan).
func scanEvent(row interface {
	Scan(dest ...any) error
}) (Event, error) {
	var e Event
	err := row.Scan(&e.ID, &e.RunID, &e.Seq, &e.FromRef, &e.ToRef,
		&e.Content, &e.Decision, &e.Summary, &e.Status, &e.CreatedAt)
	return e, err
}

// ReplaySince returns message rows with id > lastID ordered by id — the
// reconnect replay (testable core).
func ReplaySince(ctx context.Context, q store.Querier, lastID int64) ([]Event, error) {
	rows, err := q.Query(ctx,
		`SELECT `+replayColumns+` FROM message WHERE id > $1 ORDER BY id ASC`, lastID)
	if err != nil {
		return nil, fmt.Errorf("replay query: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("replay scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("replay rows: %w", err)
	}
	return out, nil
}

// loadEvent hydrates a single Event by id (used by the tail after a NOTIFY,
// which carries only the id per the ≤8KB payload budget).
func loadEvent(ctx context.Context, q store.Querier, id int64) (Event, error) {
	row := q.QueryRow(ctx,
		`SELECT `+replayColumns+` FROM message WHERE id = $1`, id)
	return scanEvent(row)
}

// ParseLastEventID reads the Last-Event-ID header (falling back to a query
// param); non-numeric/absent → 0.
func ParseLastEventID(r *http.Request) int64 {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("last_event_id")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

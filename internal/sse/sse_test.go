package sse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/giantbeaver9/yuno/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- seed helpers -----------------------------------------------------------

// seedRun inserts a run row (the FK target for message.run_id).
func seedRun(t *testing.T, pool *pgxpool.Pool, guid string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO run (guid) VALUES ($1)`, guid); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

// seedMessages inserts n message rows for the run and returns their ids in
// insertion order. A fresh testutil schema starts the IDENTITY at 1.
func seedMessages(t *testing.T, pool *pgxpool.Pool, runID string, n int) []int64 {
	t.Helper()
	ids := make([]int64, 0, n)
	for i := 1; i <= n; i++ {
		var id int64
		err := pool.QueryRow(context.Background(),
			`INSERT INTO message (run_id, seq, from_ref, to_ref, content)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			runID, int64(i), "planner", "coder", "body").Scan(&id)
		if err != nil {
			t.Fatalf("seed message %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

// --- ReplaySince (DB) -------------------------------------------------------

// [positive] Seed 5 rows: ReplaySince(0) returns all 5 in id order;
// ReplaySince(3) returns ids 4,5 only.
func TestReplaySince_ReturnsNewerRowsInOrder(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	seedRun(t, st.Pool, "run-1")
	seedMessages(t, st.Pool, "run-1", 5) // ids 1..5

	all, err := ReplaySince(ctx, st.Pool, 0)
	if err != nil {
		t.Fatalf("ReplaySince(0): %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("ReplaySince(0) len = %d, want 5", len(all))
	}
	for i, ev := range all {
		if ev.ID != int64(i+1) {
			t.Errorf("all[%d].ID = %d, want %d", i, ev.ID, i+1)
		}
	}
	if all[0].RunID != "run-1" || all[0].Seq != 1 || all[0].FromRef != "planner" {
		t.Errorf("projection wrong: %+v", all[0])
	}

	tail, err := ReplaySince(ctx, st.Pool, 3)
	if err != nil {
		t.Fatalf("ReplaySince(3): %v", err)
	}
	if len(tail) != 2 || tail[0].ID != 4 || tail[1].ID != 5 {
		t.Fatalf("ReplaySince(3) = %v, want ids [4 5]", ids(tail))
	}
}

// [negative] ReplaySince(lastID) with lastID >= max(id) -> empty slice, nil err.
func TestReplaySince_AtOrAboveMax_Empty(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	seedRun(t, st.Pool, "run-1")
	seedMessages(t, st.Pool, "run-1", 5) // max id = 5

	got, err := ReplaySince(ctx, st.Pool, 5)
	if err != nil {
		t.Fatalf("ReplaySince(5): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReplaySince(5) len = %d, want 0", len(got))
	}
	got, err = ReplaySince(ctx, st.Pool, 99)
	if err != nil {
		t.Fatalf("ReplaySince(99): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReplaySince(99) len = %d, want 0", len(got))
	}
}

// [edge] ReplaySince(0) on an empty table -> empty slice, nil error.
func TestReplaySince_EmptyTable_Empty(t *testing.T) {
	st := testutil.NewDB(t)
	got, err := ReplaySince(context.Background(), st.Pool, 0)
	if err != nil {
		t.Fatalf("ReplaySince on empty table: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty table len = %d, want 0", len(got))
	}
}

// --- ParseLastEventID -------------------------------------------------------

// [negative] non-numeric or absent Last-Event-ID -> 0.
func TestParseLastEventID_NonNumericOrAbsent_Zero(t *testing.T) {
	cases := []struct {
		name   string
		header string
		set    bool
	}{
		{"absent", "", false},
		{"empty", "", true},
		{"nonNumeric", "abc", true},
		{"negative", "-4", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/events", nil)
			if c.set {
				r.Header.Set("Last-Event-ID", c.header)
			}
			if got := ParseLastEventID(r); got != 0 {
				t.Fatalf("ParseLastEventID(%q) = %d, want 0", c.header, got)
			}
		})
	}
}

// [positive] a numeric Last-Event-ID is parsed; query-param fallback works.
func TestParseLastEventID_Numeric(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/events", nil)
	r.Header.Set("Last-Event-ID", "42")
	if got := ParseLastEventID(r); got != 42 {
		t.Fatalf("header: got %d, want 42", got)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/events?last_event_id=7", nil)
	if got := ParseLastEventID(r2); got != 7 {
		t.Fatalf("query fallback: got %d, want 7", got)
	}
}

// --- Event.SSE framing ------------------------------------------------------

// [edge] Event.SSE() starts with "id: " and ends with the blank-line
// terminator, and carries a JSON data payload.
func TestEventSSE_Framing(t *testing.T) {
	ev := Event{ID: 12, RunID: "run-1", Seq: 3, FromRef: "a", ToRef: "b", Content: "hi"}
	frame := string(ev.SSE())

	if !strings.HasPrefix(frame, "id: 12\n") {
		t.Fatalf("frame does not start with id line: %q", frame)
	}
	if !strings.HasSuffix(frame, "\n\n") {
		t.Fatalf("frame does not end with blank-line terminator: %q", frame)
	}
	if !strings.Contains(frame, "data: ") {
		t.Fatalf("frame missing data line: %q", frame)
	}
	// The data payload must be valid JSON round-tripping to the same event.
	line := ""
	for _, l := range strings.Split(frame, "\n") {
		if strings.HasPrefix(l, "data: ") {
			line = strings.TrimPrefix(l, "data: ")
		}
	}
	var back Event
	if err := json.Unmarshal([]byte(line), &back); err != nil {
		t.Fatalf("data payload not JSON: %v (%q)", err, line)
	}
	if back.ID != 12 || back.RunID != "run-1" || back.Content != "hi" {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
}

// --- hub fan-out (no Postgres) ----------------------------------------------

// [positive] every subscribed client receives a broadcast event.
func TestHub_FanoutToAllClients(t *testing.T) {
	h := newHub()
	a := h.subscribe()
	b := h.subscribe()

	h.broadcast(Event{ID: 7})

	for name, ch := range map[string]chan Event{"a": a, "b": b} {
		select {
		case ev := <-ch:
			if ev.ID != 7 {
				t.Fatalf("client %s got ID %d, want 7", name, ev.ID)
			}
		case <-time.After(time.Second):
			t.Fatalf("client %s received nothing", name)
		}
	}
}

// [edge] a slow/full client is dropped, never blocking broadcast or the
// other clients.
func TestHub_SlowClientDoesNotBlockOthers(t *testing.T) {
	h := newHub()
	slow := h.subscribe()

	// Fill the slow client's buffer to capacity without reading it.
	for i := 0; i < cap(slow); i++ {
		h.broadcast(Event{ID: int64(i)})
	}

	fast := h.subscribe()

	// One more broadcast: slow is full (must be dropped, not block), fast gets it.
	done := make(chan struct{})
	go func() {
		h.broadcast(Event{ID: 999})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on a slow client")
	}

	select {
	case ev := <-fast:
		if ev.ID != 999 {
			t.Fatalf("fast client got ID %d, want 999", ev.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("fast client did not receive after slow client filled up")
	}
}

// [edge] unsubscribe removes a client so it no longer receives.
func TestHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := newHub()
	a := h.subscribe()
	h.unsubscribe(a)

	h.broadcast(Event{ID: 5})

	select {
	case ev, ok := <-a:
		if ok {
			t.Fatalf("unsubscribed client still received ID %d", ev.ID)
		}
	default:
		// no delivery — correct.
	}
}

// --- Handler wire format (httptest + DB replay) -----------------------------

// serveUntilReplayed runs the handler in a goroutine, lets the connect-time
// replay flush, then cancels the request context so the LISTEN/NOTIFY tail
// exits, and returns the recorder.
func serveUntilReplayed(t *testing.T, h *Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()

	// Replay is written synchronously at connect; give it a moment, then
	// cancel to break out of the tail loop.
	time.Sleep(250 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after context cancel")
	}
	return rec
}

// [positive] Handler with Last-Event-ID: 3 sets the event-stream content type
// and replays only rows with id > 3, in order, with correct SSE framing.
func TestHandler_ReplayWireFormat(t *testing.T) {
	st := testutil.NewDB(t)
	seedRun(t, st.Pool, "run-1")
	seedMessages(t, st.Pool, "run-1", 5) // ids 1..5

	h := NewHandler(st.Pool)
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req.Header.Set("Last-Event-ID", "3")

	rec := serveUntilReplayed(t, h, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "id: 4\n") || !strings.Contains(body, "id: 5\n") {
		t.Fatalf("body missing replayed ids 4,5:\n%s", body)
	}
	if strings.Contains(body, "id: 3\n") || strings.Contains(body, "id: 1\n") {
		t.Fatalf("body replayed already-seen ids:\n%s", body)
	}
	// Ordering: id 4 must appear before id 5.
	if strings.Index(body, "id: 4\n") > strings.Index(body, "id: 5\n") {
		t.Fatalf("replay out of order:\n%s", body)
	}
}

// [negative] a non-numeric Last-Event-ID starts the replay from 0 (all rows)
// without panicking.
func TestHandler_NonNumericLastEventID_StartsFromZero(t *testing.T) {
	st := testutil.NewDB(t)
	seedRun(t, st.Pool, "run-1")
	seedMessages(t, st.Pool, "run-1", 3) // ids 1..3

	h := NewHandler(st.Pool)
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req.Header.Set("Last-Event-ID", "not-a-number")

	rec := serveUntilReplayed(t, h, req)

	body := rec.Body.String()
	for _, want := range []string{"id: 1\n", "id: 2\n", "id: 3\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q (replay should start from 0):\n%s", want, body)
		}
	}
}

// [edge] with no newer rows the handler replays nothing but keeps the stream
// open (returns only when the request context is cancelled) and still sets the
// content type.
func TestHandler_NoNewerRows_StreamStaysOpen(t *testing.T) {
	st := testutil.NewDB(t)
	seedRun(t, st.Pool, "run-1")
	seedMessages(t, st.Pool, "run-1", 2) // ids 1..2

	h := NewHandler(st.Pool)
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req.Header.Set("Last-Event-ID", "2") // nothing newer

	// Confirm the handler does NOT return on its own before we cancel.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("handler returned before context cancel (stream should stay open)")
	case <-time.After(300 * time.Millisecond):
		// still open — correct.
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after cancel")
	}

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	if strings.Contains(body, "id: 1\n") || strings.Contains(body, "id: 2\n") {
		t.Fatalf("nothing should have replayed for Last-Event-ID: 2:\n%s", body)
	}
}

// ids extracts the ID field of each event for readable failure messages.
func ids(evs []Event) []int64 {
	out := make([]int64, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

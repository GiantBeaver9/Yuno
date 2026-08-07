package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/giantbeaver9/yuno/internal/bus"
	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/giantbeaver9/yuno/internal/testutil"
)

// insertRun inserts a minimal parent run row so message.run_id FK is satisfied
// (mirrors internal/bus/bus_test.go's helper — bus enqueue tests need a seeded
// run row).
func insertRun(t *testing.T, ctx context.Context, q store.Querier, guid string) {
	t.Helper()
	if _, err := q.Exec(ctx, "INSERT INTO run (guid) VALUES ($1)", guid); err != nil {
		t.Fatalf("insert run %q: %v", guid, err)
	}
}

// utf16Len counts s in UTF-16 code units, the same measure Telegram enforces.
func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

// ---------------------------------------------------------------------------
// [positive] SplitMessage of a short string returns exactly one chunk.
// ---------------------------------------------------------------------------
func TestSplitMessage_ShortString_OneChunk(t *testing.T) {
	chunks := SplitMessage("hello world")
	if len(chunks) != 1 {
		t.Fatalf("SplitMessage(short) chunk count = %d, want 1", len(chunks))
	}
	if chunks[0] != "hello world" {
		t.Errorf("SplitMessage(short) = %q, want %q", chunks[0], "hello world")
	}
}

// ---------------------------------------------------------------------------
// [positive] SplitMessage of a 5000-char ASCII string: every chunk ≤ 4096
// UTF-16 units and the concatenation equals the input.
// ---------------------------------------------------------------------------
func TestSplitMessage_LongASCII_ChunksWithinLimitAndRoundTrips(t *testing.T) {
	s := strings.Repeat("a", 5000)
	chunks := SplitMessage(s)
	if len(chunks) < 2 {
		t.Fatalf("SplitMessage(5000 ascii) chunk count = %d, want >= 2", len(chunks))
	}
	var joined strings.Builder
	for i, c := range chunks {
		if n := utf16Len(c); n > maxUnits {
			t.Errorf("chunk %d: %d utf16 units, want <= %d", i, n, maxUnits)
		}
		joined.WriteString(c)
	}
	if joined.String() != s {
		t.Errorf("chunks did not round-trip to the original string")
	}
}

// ---------------------------------------------------------------------------
// [positive] Allowed(123, []int64{123}) -> true.
// ---------------------------------------------------------------------------
func TestAllowed_ChatInAllowlist(t *testing.T) {
	if !Allowed(123, []int64{123}) {
		t.Errorf("Allowed(123, [123]) = false, want true")
	}
}

// ---------------------------------------------------------------------------
// [positive] InboundToEnqueue on a private text update maps to the expected
// EnqueueParams.
// ---------------------------------------------------------------------------
func TestInboundToEnqueue_PrivateText(t *testing.T) {
	u := Update{ChatID: 555, Text: "hi", IsPrivate: true}
	got, ok := InboundToEnqueue(u, 7)
	if !ok {
		t.Fatalf("InboundToEnqueue(private text) ok = false, want true")
	}
	want := bus.EnqueueParams{FromRef: "human:telegram:555", ToRef: "agent:7", Content: "hi"}
	if got.FromRef != want.FromRef || got.ToRef != want.ToRef || got.Content != want.Content {
		t.Errorf("InboundToEnqueue = %+v, want %+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// [positive] Integration: an inbound update from an allowlisted chat, polled
// through Bot, actually lands as a bus row on a seeded run.
// ---------------------------------------------------------------------------
func TestBot_Poll_AllowlistedChat_EnqueuesOntoBus(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	insertRun(t, ctx, st.Pool, "run-A")

	srv := newFakeTelegramServer(t, []fakeUpdate{
		{UpdateID: 1, ChatID: 555, Text: "hello from telegram", Type: "private"},
	})
	defer srv.Close()

	client := NewClient("test-token", srv.Client())
	client.base = srv.URL

	b := bus.New(st.Pool)
	bot := NewBot(client, b, []int64{555}, 7, "run-A")

	if err := bot.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	var fromRef, toRef, content string
	err := st.Pool.QueryRow(ctx,
		"SELECT from_ref, to_ref, content FROM message WHERE run_id=$1 AND from_ref=$2",
		"run-A", "human:telegram:555").Scan(&fromRef, &toRef, &content)
	if err != nil {
		t.Fatalf("expected enqueued bus row for allowlisted chat: %v", err)
	}
	if toRef != "agent:7" || content != "hello from telegram" {
		t.Errorf("bus row = {to_ref:%q content:%q}, want {agent:7, hello from telegram}", toRef, content)
	}
}

// ---------------------------------------------------------------------------
// [negative] Allowed(999, []int64{123}) -> false.
// ---------------------------------------------------------------------------
func TestAllowed_ChatNotInAllowlist(t *testing.T) {
	if Allowed(999, []int64{123}) {
		t.Errorf("Allowed(999, [123]) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// [negative] InboundToEnqueue rejects a non-private (group) update.
// ---------------------------------------------------------------------------
func TestInboundToEnqueue_RejectsGroupUpdate(t *testing.T) {
	u := Update{ChatID: 555, Text: "hi", IsPrivate: false}
	_, ok := InboundToEnqueue(u, 7)
	if ok {
		t.Errorf("InboundToEnqueue(group update) ok = true, want false")
	}
}

// ---------------------------------------------------------------------------
// [negative] InboundToEnqueue rejects a non-text (empty text) update.
// ---------------------------------------------------------------------------
func TestInboundToEnqueue_RejectsNonTextUpdate(t *testing.T) {
	u := Update{ChatID: 555, Text: "", IsPrivate: true}
	_, ok := InboundToEnqueue(u, 7)
	if ok {
		t.Errorf("InboundToEnqueue(empty text) ok = true, want false")
	}
}

// ---------------------------------------------------------------------------
// [negative] A chat NOT on the allowlist is rejected end-to-end: no bus row
// is written for it.
// ---------------------------------------------------------------------------
func TestBot_Poll_ChatNotAllowlisted_NoEnqueue(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	insertRun(t, ctx, st.Pool, "run-A")

	srv := newFakeTelegramServer(t, []fakeUpdate{
		{UpdateID: 1, ChatID: 999, Text: "intruder", Type: "private"},
	})
	defer srv.Close()

	client := NewClient("test-token", srv.Client())
	client.base = srv.URL

	b := bus.New(st.Pool)
	bot := NewBot(client, b, []int64{555}, 7, "run-A") // 999 is not allowlisted

	if err := bot.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	var count int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM message WHERE run_id=$1", "run-A").Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 0 {
		t.Errorf("message count for disallowed chat = %d, want 0 (no bus row)", count)
	}
}

// ---------------------------------------------------------------------------
// [negative] An error path (unreachable Bot API base) redacts the bot token
// from the returned error.
// ---------------------------------------------------------------------------
func TestClient_ErrorRedactsToken(t *testing.T) {
	const token = "123456:SUPER-SECRET-TOKEN"
	client := NewClient(token, &http.Client{})
	// Point at a base URL nothing is listening on so http.Do fails with a
	// *url.Error embedding the token-bearing request URL.
	client.base = "http://127.0.0.1:1"

	_, err := client.GetUpdates(context.Background(), 0)
	if err == nil {
		t.Fatalf("GetUpdates against unreachable base: err = nil, want non-nil")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error leaks bot token: %v", err)
	}
}

// ---------------------------------------------------------------------------
// [negative] Redact directly removes the token substring from a string.
// ---------------------------------------------------------------------------
func TestRedact_RemovesToken(t *testing.T) {
	const token = "abc123token"
	s := fmt.Sprintf("Post \"https://api.telegram.org/bot%s/getUpdates\": dial tcp: connect: connection refused", token)
	got := Redact(token, s)
	if strings.Contains(got, token) {
		t.Errorf("Redact left the token in place: %q", got)
	}
}

// ---------------------------------------------------------------------------
// [edge] SplitMessage of a string of astral emoji (2 UTF-16 units each) keeps
// every chunk ≤ 4096 units where a naive rune count would overshoot, and
// round-trips with no lost content.
// ---------------------------------------------------------------------------
func TestSplitMessage_AstralEmoji_RespectsUTF16Limit(t *testing.T) {
	const emoji = "😀"                // U+1F600, 2 UTF-16 units, 1 rune
	s := strings.Repeat(emoji, 3000) // 3000 runes, 6000 UTF-16 units
	if got := utf16Len(s); got != 6000 {
		t.Fatalf("test setup: utf16Len(s) = %d, want 6000", got)
	}
	// A naive rune-count split at 4096 runes would NOT overshoot maxUnits
	// (4096 runes = 8192 units already over — the real bug a naive
	// implementation would show is emitting a single chunk that is under
	// the *rune* limit yet over the UTF-16 unit limit). Assert every chunk
	// is measured in UTF-16 units.
	chunks := SplitMessage(s)
	if len(chunks) < 2 {
		t.Fatalf("SplitMessage(6000 units of emoji) chunk count = %d, want >= 2", len(chunks))
	}
	var joined strings.Builder
	for i, c := range chunks {
		if n := utf16Len(c); n > maxUnits {
			t.Errorf("chunk %d: %d utf16 units, want <= %d", i, n, maxUnits)
		}
		joined.WriteString(c)
	}
	if joined.String() != s {
		t.Errorf("emoji chunks did not round-trip to the original string (lost or corrupted content)")
	}
}

// ---------------------------------------------------------------------------
// [edge] A single 5000-char line with no '\n' is hard-split into ≤4096-unit
// pieces (no newline boundary available to prefer).
// ---------------------------------------------------------------------------
func TestSplitMessage_NoNewline_HardSplits(t *testing.T) {
	s := strings.Repeat("x", 5000) // one line, no '\n' anywhere
	chunks := SplitMessage(s)
	if len(chunks) < 2 {
		t.Fatalf("SplitMessage(5000 no-newline) chunk count = %d, want >= 2", len(chunks))
	}
	var joined strings.Builder
	for i, c := range chunks {
		if strings.Contains(c, "\n") {
			t.Errorf("chunk %d unexpectedly contains a newline", i)
		}
		if n := utf16Len(c); n > maxUnits {
			t.Errorf("chunk %d: %d utf16 units, want <= %d", i, n, maxUnits)
		}
		joined.WriteString(c)
	}
	if joined.String() != s {
		t.Errorf("no-newline chunks did not round-trip to the original string")
	}
}

// ---------------------------------------------------------------------------
// [edge] Empty allowlist = discovery mode: any chat id is accepted.
// ---------------------------------------------------------------------------
func TestAllowed_EmptyAllowlist_IsDiscoveryMode(t *testing.T) {
	if !Allowed(1, nil) {
		t.Errorf("Allowed(1, nil) = false, want true (empty allowlist = discovery mode)")
	}
	if !Allowed(999999, []int64{}) {
		t.Errorf("Allowed(999999, []) = false, want true (empty allowlist = discovery mode)")
	}
}

// ---------------------------------------------------------------------------
// test fakes: a minimal Bot API server for GetUpdates/SendMessage tests.
// ---------------------------------------------------------------------------

type fakeUpdate struct {
	UpdateID int64
	ChatID   int64
	Text     string
	Type     string // "private" | "group"
}

// newFakeTelegramServer serves getUpdates (returning the given updates once)
// and sendMessage (recording nothing, always OK) in the Bot API wire shape.
func newFakeTelegramServer(t *testing.T, updates []fakeUpdate) *httptest.Server {
	t.Helper()
	served := false
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			var result []map[string]any
			if !served {
				served = true
				for _, u := range updates {
					result = append(result, map[string]any{
						"update_id": u.UpdateID,
						"message": map[string]any{
							"text": u.Text,
							"chat": map[string]any{
								"id":   u.ChatID,
								"type": u.Type,
							},
						},
					})
				}
			}
			raw, _ := json.Marshal(result)
			resp := map[string]any{"ok": true, "result": json.RawMessage(raw)}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			resp := map[string]any{"ok": true, "result": json.RawMessage(`{}`)}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	})
	return httptest.NewServer(mux)
}

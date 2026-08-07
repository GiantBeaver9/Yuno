# sse — live run monitor (SSE)

`internal/sse` streams the run bus to the browser as [Server-Sent Events](https://developer.mozilla.org/docs/Web/API/Server-sent_events).
A client (the monitor UI) opens one long-lived `GET`, receives every new bus
message as it happens, and can reconnect after a drop without losing or
duplicating a single event. That guarantee comes from three cooperating layers.

## Layer 1 — the durable log (`message.id`)

Every bus message is a row in the `message` table. Its primary key
`id BIGINT GENERATED ALWAYS AS IDENTITY` is a **global, monotonic cursor**: it
only ever increases, and it orders all messages across all runs. That id is the
SSE event id we put on the wire (`id: <n>`), so the browser's built-in
`Last-Event-ID` bookkeeping becomes a cursor into our durable log for free.

`Event` is a local projection of the columns we serve
(`id, run_id, seq, from_ref, to_ref, content, decision, summary, status,
created_at`). It is deliberately **not** an import of `bus` — `sse` stays a
store-only leaf, so the wire shape can evolve without coupling to the bus type.

`Event.SSE()` renders one frame:

```
id: 42
data: {"ID":42,"RunID":"run-1","Seq":3, ...}

```

(terminated by the blank line SSE requires).

## Layer 2 — the LISTEN/NOTIFY tail

New rows have to reach connected clients live. On write, the message id is
published on the Postgres `NOTIFY` channel `yuno_messages` (const `Channel`):

```go
NotifyPublisher.Publish(ctx, messageID)  // SELECT pg_notify('yuno_messages', '42')
```

The payload is **the id only** (Postgres caps NOTIFY at ~8 KB); receivers
hydrate the full row by id. `Publish` should run in the **same transaction** as
the message insert, so a client can never be told about a row that then rolls
back — the tail can't diverge from the log.

On the read side the `Handler` keeps **one** `LISTEN` connection shared by all
connected clients. Its listener goroutine waits for notifications, hydrates each
id (`loadEvent`), and hands the `Event` to an in-memory **hub** that fans it out
to every subscribed client. The hub sends non-blocking with a small per-client
buffer: a slow or vanished client is *dropped* on overflow rather than stalling
the listener or the other clients — it will catch up via replay on reconnect
(Layer 3). The shared listener is reference-counted: the first client starts it,
the last client to leave cancels its context, which releases the pooled
connection (so `pool.Close` never blocks on a checked-out connection).

## Layer 3 — Last-Event-ID replay

When a client connects (or reconnects), `ParseLastEventID` reads its
`Last-Event-ID` header (query-param `last_event_id` as a fallback; non-numeric
or absent → `0`). `ReplaySince(ctx, q, lastID)` then returns exactly the durable
rows with `id > lastID`, ordered ascending. The handler writes those frames
first, then joins the live tail — deduping the overlap by id so an event seen in
both replay and the tail is written once. The result is a **gap-free, no-dup**
continuation across reconnects.

The handler subscribes to the hub *before* replaying, so a message committed
during the replay window is buffered on the client's channel instead of being
lost between the two phases.

## The Publisher seam (ADR-23)

```go
type Publisher interface {
    Publish(ctx context.Context, messageID int64) error
}
```

`NotifyPublisher` (Postgres `pg_notify`) is the default. The one-method seam
means a future Redis / NATS backend is a drop-in: implement `Publish`, and the
transport for Layer 2 changes with **no rewrite** of the log (Layer 1) or the
replay/handler (Layer 3). Only the tail's notification source would move behind
the same interface.

## What is tested, and what isn't

- **Hub fan-out** is pure in-memory — tested without Postgres (fan-out to all
  clients, slow-client-drop, unsubscribe).
- **`ReplaySince` + the SSE wire format** are tested against a real schema via
  `testutil.NewDB` and `httptest` (replay ordering, `id>Last-Event-ID`
  windowing, framing, content-type, stream-stays-open).
- The **LISTEN/NOTIFY round-trip** is intentionally given only light coverage:
  the handler tests exercise starting and cleanly tearing down the listener
  (via request-context cancel), but a full NOTIFY→client delivery is left to
  integration, since `pg_notify` is database-wide (not schema-scoped) and would
  cross `testutil`'s per-test schemas.

# bus — the message table as queue + trail

The `bus` package turns the single `message` table into two things at once: a
**work queue** (rows are claimed, leased, and retired) and an append-only
**trail** (every row is a permanent breadcrumb of what happened in a run). There
is no separate queue backend — the queue *is* the table, indexed by
`(status, lease_until)` for claiming and `(run_id, seq)` for replay.

All methods run over a `store.Querier`, which is satisfied by both
`*pgxpool.Pool` and `pgx.Tx`. This lets a call run standalone on the pool or
compose inside a caller-owned transaction. `Bus` (Enqueue / ClaimNext /
ReclaimExpired) is built with `New(q)`; `Ack` is a free function taking the
querier explicitly so it slots into the orchestrator's transaction.

## Per-run seq

Every message carries a `seq` that is monotonic **per run** (independent across
runs). Callers never set it: both `Enqueue` and `Ack`'s output inserts assign it
inline with `COALESCE(MAX(seq), 0) + 1 WHERE run_id = $1`, returning the full
row. The first message of a run is seq 1; a different run starts again at 1.
`seq` is the per-run replay cursor; the table's global `id` is the SSE cursor.

## Queue model — lease claim

A message moves through `queued → processing → done`. `ClaimNext` claims the
oldest claimable row in two statements on the querier:

1. `SELECT id FROM message WHERE status='queued' ORDER BY id FOR UPDATE SKIP
   LOCKED LIMIT 1` — takes the oldest queued row and row-locks it, **skipping**
   any row another claimer already holds.
2. `UPDATE ... SET status='processing', lease_until = now() + lease RETURNING …`
   — flips it to processing with a fresh lease and returns the row.

When nothing is claimable it returns `(nil, nil)`.

**Single-winner concurrency.** When the `Bus` wraps a `pgx.Tx`, the `FOR UPDATE`
lock is held for that transaction's lifetime, so two overlapping transactions
that both call `ClaimNext` against one queued row resolve to exactly one winner —
the loser's `SKIP LOCKED` skips the locked row and it gets `(nil, nil)`. On a
bare pool the lock releases immediately after the SELECT, which is fine for the
non-concurrent path.

**Lease expiry.** A claimed row is only leased, not owned forever. If a worker
dies, its `lease_until` passes. `ReclaimExpired` runs one UPDATE reverting every
`processing` row with `lease_until < now()` back to `queued` (clearing the
lease) and returns the count reverted. Rows still within their lease are left
untouched, so a healthy in-flight message is never yanked away.

## Trail model — atomic ack

`Ack` retires a claimed input and records everything that followed from it, as a
single unit of four writes on the caller's querier:

1. **Mark input done** — `UPDATE message SET status='done' WHERE id=$1 AND
   status='processing'`. If the row is not still processing (already acked, or
   never claimed), the update touches 0 rows and `Ack` returns an error, writing
   nothing further.
2. **Enqueue outputs** — 0..n follow-on messages, each queued with the next
   per-run seq.
3. **Push a stack breadcrumb** — one `stack` row `(run_id, seq, summary,
   ticket_id)`, seq monotonic per run within the stack.
4. **Upsert the dict payload** — `INSERT INTO dict … ON CONFLICT (guid) DO UPDATE
   SET full_detail = EXCLUDED.full_detail`. A `nil` `DictDetail` skips this write.

`Ack` never begins or commits — the **caller owns the transaction boundary**.
Passed a `pgx.Tx`, the four writes are all-or-nothing: commit persists the input
as done plus the outputs, stack, and dict together; rollback (or an error raised
mid-ack) leaves the input still `processing` and none of the output/stack/dict
rows behind. That atomicity is what lets a message be a durable trail entry
rather than a lossy in-memory event.

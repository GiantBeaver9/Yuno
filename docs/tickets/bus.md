# bus — messages table as queue + trail (lease claim, atomic ack)

## Unit
bus

## Package / Owned files
`internal/bus/*.go`

## Deps
(none beyond store — leaf)

## Tier
hard

## Interfaces
```go
package bus

import (
	"context"
	"encoding/json"
	"time"

	"github.com/giantbeaver9/yuno/internal/store"
)

// Message mirrors a row of the `message` table (schema.sql §5).
type Message struct {
	ID         int64
	RunID      string // message.run_id  (→ run.guid)
	Seq        int64
	FromRef    string // "agent:<id>" | "human:telegram:<chatid>" | "system:schedule"
	ToRef      string
	Content    string
	Decision   string // "" | approve | reject | complete
	Summary    string
	Status     string // queued|processing|done|pending_approval|needs_human
	LeaseUntil *time.Time
	Tokens     int
	Cost       float64
	CreatedAt  time.Time
}

// EnqueueParams is a new queued message. Seq is assigned per-run monotonically
// by Enqueue/Ack (COALESCE(MAX(seq),0)+1 WHERE run_id=$1); callers never set it.
type EnqueueParams struct {
	RunID   string
	FromRef string
	ToRef   string
	Content string
	Decision string // usually "" on enqueue; set on agent outputs
	Summary  string
	Tokens   int
	Cost     float64
}

type Bus struct{ /* q store.Querier */ }

func New(q store.Querier) *Bus

// Enqueue inserts a queued row with the next per-run seq. RETURNING the full row.
func (b *Bus) Enqueue(ctx context.Context, p EnqueueParams) (Message, error)

// ClaimNext atomically claims the oldest queued row (by id) whose lease is free:
// SELECT ... WHERE status='queued' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1,
// then UPDATE status='processing', lease_until=now()+lease. Returns (nil,nil)
// when nothing is claimable.
func (b *Bus) ClaimNext(ctx context.Context, lease time.Duration) (*Message, error)

// ReclaimExpired reverts processing rows whose lease_until < now() back to queued.
// Returns the count reverted.
func (b *Bus) ReclaimExpired(ctx context.Context) (int64, error)

// AckParams is the atomic-ack bundle: mark the input done, enqueue output(s),
// push a stack breadcrumb, upsert the dict payload — all in ONE transaction.
type AckParams struct {
	InputID       int64           // message.id to mark done (must still be 'processing')
	RunID         string
	Outputs       []EnqueueParams // 0..n follow-on messages
	StackSummary  string
	StackTicketID *int64          // nullable (stack.ticket_id)
	DictGuid      string          // dict.guid (usually the run guid)
	DictDetail    json.RawMessage // dict.full_detail (JSONB); nil skips the upsert
}

// Ack runs the four writes on the caller-provided Querier so it composes inside
// the orchestrator's pgx.Tx (store.Querier is satisfied by pgx.Tx). It does NOT
// begin/commit — the caller owns the transaction boundary.
func Ack(ctx context.Context, q store.Querier, p AckParams) error
```

## Accept
- `Enqueue` assigns `seq` = 1 for the first message of a run, incrementing per subsequent message of the same run; distinct runs have independent seq sequences.
- `ClaimNext` moves exactly one `queued` row to `processing`, sets `lease_until = now()+lease`, and returns it; concurrent claimers never get the same row (`FOR UPDATE SKIP LOCKED`).
- `Ack` (inside a tx) writes output message rows, one `stack` row (`run_id, seq, summary, ticket_id`), upserts `dict` (`ON CONFLICT (guid) DO UPDATE SET full_detail=EXCLUDED.full_detail`), and updates the input row to `status='done'` — atomically. Rollback leaves the input still `processing` and no output rows.
- `ReclaimExpired` only touches `processing` rows past their lease.

## Test cases
All DB tests use `testutil.NewDB`. Each must first insert a `run` row (message.run_id → run.guid FK); insert project→build→ticket→run or a minimal run with a guid.
- **[positive]** Enqueue two messages for one run → seqs `1`,`2`; `ClaimNext` returns the first, flipping it to `processing` with a future `lease_until`; open a tx, call `Ack` (one output + stack + dict + mark input done), commit → input row is `done`, output row exists as `queued` with seq `3`, `stack` has the breadcrumb, `dict` has the payload.
- **[negative]** Begin a tx, call `Ack`, then `Rollback` (or force an error mid-Ack) → input row is still `processing`, no output/stack/dict rows persisted (atomicity). `ClaimNext` on a run with no queued rows → `(nil, nil)`.
- **[edge]** Two overlapping transactions each call `ClaimNext` against a single queued row → exactly one gets it, the other gets `(nil,nil)` (SKIP LOCKED). `ReclaimExpired` reverts a `processing` row whose `lease_until` is in the past but leaves a still-leased `processing` row untouched.

## Salvage
Net-new (PORT.md "Net-new" #4 lease queue, #5 atomic ack — the Pi repo has no `Begin/Commit`). LIFT the shared-scanner idiom (events.go:35-57) for `Message` scanning and the `ON CONFLICT DO UPDATE` upsert (prefs.go:17) for the dict write; the atomic-ack tx skeleton is in PORT.md §3.

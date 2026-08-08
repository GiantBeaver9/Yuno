// Package bus turns the single `message` table into both a work queue and an
// append-only trail. A message row is a queue entry (status queued→processing→
// done, guarded by a lease) and, once written, a permanent breadcrumb of what
// happened. There is no separate queue store: enqueue writes a row, ClaimNext
// leases the oldest queued row via FOR UPDATE SKIP LOCKED so concurrent workers
// never claim the same one, and Ack retires an input while atomically emitting
// its follow-on messages, a stack breadcrumb, and a dict payload — composed on a
// caller-owned transaction so the whole acknowledgement commits or not at all.
package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/jackc/pgx/v5"
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
	RunID    string
	FromRef  string
	ToRef    string
	Content  string
	Decision string // usually "" on enqueue; set on agent outputs
	Summary  string
	Tokens   int
	Cost     float64
}

// messageCols is the fixed column order used by every RETURNING/scan of a row.
const messageCols = `id, run_id, seq, from_ref, to_ref, content, decision, summary, status, lease_until, tokens, cost, created_at`

// insertSQL inserts a queued row, computing the next per-run seq inline so a
// single round-trip both assigns the seq and returns the full row.
const insertSQL = `
INSERT INTO message (run_id, seq, from_ref, to_ref, content, decision, summary, status, tokens, cost)
VALUES ($1,
        (SELECT COALESCE(MAX(seq), 0) + 1 FROM message WHERE run_id = $1),
        $2, $3, $4, $5, $6, 'queued', $7, $8)
RETURNING ` + messageCols

// scanMessage scans one row in messageCols order.
func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.RunID, &m.Seq, &m.FromRef, &m.ToRef, &m.Content,
		&m.Decision, &m.Summary, &m.Status, &m.LeaseUntil, &m.Tokens, &m.Cost, &m.CreatedAt)
	return m, err
}

// insertMessage writes one queued row on the given querier (pool or tx).
func insertMessage(ctx context.Context, q store.Querier, p EnqueueParams) (Message, error) {
	row := q.QueryRow(ctx, insertSQL,
		p.RunID, p.FromRef, p.ToRef, p.Content, p.Decision, p.Summary, p.Tokens, p.Cost)
	m, err := scanMessage(row)
	if err != nil {
		return Message{}, fmt.Errorf("enqueue message: %w", err)
	}
	return m, nil
}

// Bus is a queue+trail façade over a store.Querier (a pool or a pgx.Tx).
type Bus struct{ q store.Querier }

// New returns a Bus over the given querier.
func New(q store.Querier) *Bus { return &Bus{q: q} }

// Enqueue inserts a queued row with the next per-run seq and returns it.
func (b *Bus) Enqueue(ctx context.Context, p EnqueueParams) (Message, error) {
	return insertMessage(ctx, b.q, p)
}

// claimSelectSQL locks the oldest claimable queued row for this transaction,
// skipping any row already locked by a concurrent claimer.
const claimSelectSQL = `
SELECT id FROM message
WHERE status = 'queued'
ORDER BY id
FOR UPDATE SKIP LOCKED
LIMIT 1`

// claimUpdateSQL flips the locked row to processing with a fresh lease.
const claimUpdateSQL = `
UPDATE message
SET status = 'processing', lease_until = now() + make_interval(secs => $2)
WHERE id = $1
RETURNING ` + messageCols

// ClaimNext atomically claims the oldest queued row (by id) whose lease is free:
// SELECT ... WHERE status='queued' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1,
// then UPDATE status='processing', lease_until=now()+lease. Returns (nil,nil)
// when nothing is claimable. When the Bus wraps a pgx.Tx the FOR UPDATE lock is
// held for that transaction, giving concurrent claimers single-winner semantics.
func (b *Bus) ClaimNext(ctx context.Context, lease time.Duration) (*Message, error) {
	var id int64
	if err := b.q.QueryRow(ctx, claimSelectSQL).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("claim select: %w", err)
	}
	row := b.q.QueryRow(ctx, claimUpdateSQL, id, lease.Seconds())
	m, err := scanMessage(row)
	if err != nil {
		return nil, fmt.Errorf("claim update: %w", err)
	}
	return &m, nil
}

// reclaimSQL reverts expired-lease processing rows back to the queue.
const reclaimSQL = `
UPDATE message
SET status = 'queued', lease_until = NULL
WHERE status = 'processing' AND lease_until < now()`

// ReclaimExpired reverts processing rows whose lease_until < now() back to
// queued. Returns the count reverted.
func (b *Bus) ReclaimExpired(ctx context.Context) (int64, error) {
	tag, err := b.q.Exec(ctx, reclaimSQL)
	if err != nil {
		return 0, fmt.Errorf("reclaim expired: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AckParams is the atomic-ack bundle: mark the input done, enqueue output(s),
// push a stack breadcrumb, upsert the dict payload — all in ONE transaction.
type AckParams struct {
	InputID       int64 // message.id to mark done (must still be 'processing')
	RunID         string
	Outputs       []EnqueueParams // 0..n follow-on messages
	StackSummary  string
	StackTicketID *int64          // nullable (stack.ticket_id)
	DictGuid      string          // dict.guid (usually the run guid)
	DictDetail    json.RawMessage // dict.full_detail (JSONB); nil skips the upsert
}

const (
	ackDoneSQL = `UPDATE message SET status = 'done' WHERE id = $1 AND status = 'processing'`

	ackStackSQL = `
INSERT INTO stack (run_id, seq, summary, ticket_id)
VALUES ($1,
        (SELECT COALESCE(MAX(seq), 0) + 1 FROM stack WHERE run_id = $1),
        $2, $3)`

	ackDictSQL = `
INSERT INTO dict (guid, full_detail)
VALUES ($1, $2)
ON CONFLICT (guid) DO UPDATE SET full_detail = EXCLUDED.full_detail`
)

// Ack runs the four writes on the caller-provided Querier so it composes inside
// the orchestrator's pgx.Tx (store.Querier is satisfied by pgx.Tx). It does NOT
// begin/commit — the caller owns the transaction boundary. If the input row is
// not still 'processing', Ack returns an error and writes nothing further, so
// the caller can roll back.
func Ack(ctx context.Context, q store.Querier, p AckParams) error {
	// 1. Retire the input — only if it is still processing (guards double-ack).
	tag, err := q.Exec(ctx, ackDoneSQL, p.InputID)
	if err != nil {
		return fmt.Errorf("ack mark done: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("ack: input %d not in processing state", p.InputID)
	}

	// 2. Enqueue follow-on outputs, each with the next per-run seq.
	for i := range p.Outputs {
		if _, err := insertMessage(ctx, q, p.Outputs[i]); err != nil {
			return fmt.Errorf("ack enqueue output %d: %w", i, err)
		}
	}

	// 3. Push the stack breadcrumb.
	if _, err := q.Exec(ctx, ackStackSQL, p.RunID, p.StackSummary, p.StackTicketID); err != nil {
		return fmt.Errorf("ack stack: %w", err)
	}

	// 4. Upsert the dict payload (nil detail skips it).
	if p.DictDetail != nil {
		if _, err := q.Exec(ctx, ackDictSQL, p.DictGuid, []byte(p.DictDetail)); err != nil {
			return fmt.Errorf("ack dict: %w", err)
		}
	}

	return nil
}

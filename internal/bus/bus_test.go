package bus_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/giantbeaver9/yuno/internal/bus"
	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/giantbeaver9/yuno/internal/testutil"
)

// insertRun inserts a minimal parent run row so message.run_id FK is satisfied.
func insertRun(t *testing.T, ctx context.Context, q store.Querier, guid string) {
	t.Helper()
	if _, err := q.Exec(ctx, "INSERT INTO run (guid) VALUES ($1)", guid); err != nil {
		t.Fatalf("insert run %q: %v", guid, err)
	}
}

// ---------------------------------------------------------------------------
// [positive] Enqueue assigns per-run monotonic seq; distinct runs are
// independent.
// ---------------------------------------------------------------------------
func TestEnqueueAssignsPerRunSeq(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	insertRun(t, ctx, st.Pool, "run-A")
	insertRun(t, ctx, st.Pool, "run-B")

	b := bus.New(st.Pool)

	m1, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "system:schedule", ToRef: "agent:1", Content: "first"})
	if err != nil {
		t.Fatalf("enqueue m1: %v", err)
	}
	if m1.Seq != 1 {
		t.Errorf("first message of run-A: seq = %d, want 1", m1.Seq)
	}
	if m1.Status != "queued" {
		t.Errorf("enqueued status = %q, want queued", m1.Status)
	}
	if m1.RunID != "run-A" || m1.Content != "first" {
		t.Errorf("row not round-tripped: %+v", m1)
	}

	m2, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "agent:1", ToRef: "agent:2", Content: "second"})
	if err != nil {
		t.Fatalf("enqueue m2: %v", err)
	}
	if m2.Seq != 2 {
		t.Errorf("second message of run-A: seq = %d, want 2", m2.Seq)
	}

	// Distinct run has an independent seq sequence.
	mB, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-B", FromRef: "system:schedule", ToRef: "agent:1", Content: "b-first"})
	if err != nil {
		t.Fatalf("enqueue mB: %v", err)
	}
	if mB.Seq != 1 {
		t.Errorf("first message of run-B: seq = %d, want 1 (independent per-run seq)", mB.Seq)
	}
}

// ---------------------------------------------------------------------------
// [positive] ClaimNext flips the oldest queued row to processing with a future
// lease and returns it.
// ---------------------------------------------------------------------------
func TestClaimNextFlipsToProcessing(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	insertRun(t, ctx, st.Pool, "run-A")

	b := bus.New(st.Pool)
	first, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "system:schedule", ToRef: "agent:1", Content: "first"})
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if _, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "system:schedule", ToRef: "agent:1", Content: "second"}); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	before := time.Now()
	claimed, err := b.ClaimNext(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claimnext: %v", err)
	}
	if claimed == nil {
		t.Fatal("claimnext returned nil, want the oldest queued row")
	}
	if claimed.ID != first.ID {
		t.Errorf("claimed id = %d, want oldest %d", claimed.ID, first.ID)
	}
	if claimed.Status != "processing" {
		t.Errorf("claimed status = %q, want processing", claimed.Status)
	}
	if claimed.LeaseUntil == nil {
		t.Fatal("claimed lease_until is nil, want a future lease")
	}
	if !claimed.LeaseUntil.After(before) {
		t.Errorf("lease_until %v is not in the future (before=%v)", claimed.LeaseUntil, before)
	}

	// Verify the flip persisted.
	var status string
	if err := st.Pool.QueryRow(ctx, "SELECT status FROM message WHERE id=$1", first.ID).Scan(&status); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if status != "processing" {
		t.Errorf("persisted status = %q, want processing", status)
	}
}

// ---------------------------------------------------------------------------
// [positive] Ack inside a tx: mark input done, enqueue output, push stack
// breadcrumb, upsert dict — atomically committed.
// ---------------------------------------------------------------------------
func TestAckAtomicCommit(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	insertRun(t, ctx, st.Pool, "run-A")

	b := bus.New(st.Pool)
	if _, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "system:schedule", ToRef: "agent:1", Content: "input"}); err != nil {
		t.Fatalf("enqueue input: %v", err)
	}
	if _, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "system:schedule", ToRef: "agent:1", Content: "other"}); err != nil {
		t.Fatalf("enqueue other: %v", err)
	}
	input, err := b.ClaimNext(ctx, time.Minute)
	if err != nil || input == nil {
		t.Fatalf("claimnext: %v (%v)", err, input)
	}

	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	detail := json.RawMessage(`{"k":"v"}`)
	err = bus.Ack(ctx, tx, bus.AckParams{
		InputID:      input.ID,
		RunID:        "run-A",
		Outputs:      []bus.EnqueueParams{{RunID: "run-A", FromRef: "agent:1", ToRef: "agent:2", Content: "output", Decision: "complete"}},
		StackSummary: "did the thing",
		DictGuid:     "run-A",
		DictDetail:   detail,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("ack: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Input marked done.
	var inStatus string
	if err := st.Pool.QueryRow(ctx, "SELECT status FROM message WHERE id=$1", input.ID).Scan(&inStatus); err != nil {
		t.Fatalf("read input: %v", err)
	}
	if inStatus != "done" {
		t.Errorf("input status = %q, want done", inStatus)
	}

	// Output row exists as queued with seq 3 (max was 2).
	var outSeq int64
	var outStatus, outContent string
	err = st.Pool.QueryRow(ctx, "SELECT seq, status, content FROM message WHERE run_id=$1 AND content='output'", "run-A").Scan(&outSeq, &outStatus, &outContent)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if outSeq != 3 {
		t.Errorf("output seq = %d, want 3", outSeq)
	}
	if outStatus != "queued" {
		t.Errorf("output status = %q, want queued", outStatus)
	}

	// Stack breadcrumb persisted.
	var stackSummary string
	if err := st.Pool.QueryRow(ctx, "SELECT summary FROM stack WHERE run_id=$1", "run-A").Scan(&stackSummary); err != nil {
		t.Fatalf("read stack: %v", err)
	}
	if stackSummary != "did the thing" {
		t.Errorf("stack summary = %q, want 'did the thing'", stackSummary)
	}

	// Dict payload upserted.
	var full []byte
	if err := st.Pool.QueryRow(ctx, "SELECT full_detail FROM dict WHERE guid=$1", "run-A").Scan(&full); err != nil {
		t.Fatalf("read dict: %v", err)
	}
	if string(full) != `{"k": "v"}` && string(full) != `{"k":"v"}` {
		t.Errorf("dict full_detail = %s, want {\"k\":\"v\"}", full)
	}
}

// ---------------------------------------------------------------------------
// [negative] Ack staged in a tx that is rolled back leaves the input still
// processing and no output/stack/dict rows.
// ---------------------------------------------------------------------------
func TestAckRollbackAtomicity(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	insertRun(t, ctx, st.Pool, "run-A")

	b := bus.New(st.Pool)
	if _, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "system:schedule", ToRef: "agent:1", Content: "input"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	input, err := b.ClaimNext(ctx, time.Minute)
	if err != nil || input == nil {
		t.Fatalf("claimnext: %v (%v)", err, input)
	}

	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	err = bus.Ack(ctx, tx, bus.AckParams{
		InputID:      input.ID,
		RunID:        "run-A",
		Outputs:      []bus.EnqueueParams{{RunID: "run-A", FromRef: "agent:1", ToRef: "agent:2", Content: "output"}},
		StackSummary: "should vanish",
		DictGuid:     "run-A",
		DictDetail:   json.RawMessage(`{"x":1}`),
	})
	if err != nil {
		t.Fatalf("ack (pre-rollback): %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Input still processing (the done-mark rolled back).
	var inStatus string
	if err := st.Pool.QueryRow(ctx, "SELECT status FROM message WHERE id=$1", input.ID).Scan(&inStatus); err != nil {
		t.Fatalf("read input: %v", err)
	}
	if inStatus != "processing" {
		t.Errorf("input status = %q, want processing after rollback", inStatus)
	}

	// No output row.
	var outCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM message WHERE content='output'").Scan(&outCount); err != nil {
		t.Fatalf("count outputs: %v", err)
	}
	if outCount != 0 {
		t.Errorf("output rows = %d, want 0 after rollback", outCount)
	}

	// No stack row.
	var stackCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM stack WHERE run_id=$1", "run-A").Scan(&stackCount); err != nil {
		t.Fatalf("count stack: %v", err)
	}
	if stackCount != 0 {
		t.Errorf("stack rows = %d, want 0 after rollback", stackCount)
	}

	// No dict row.
	var dictCount int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM dict WHERE guid=$1", "run-A").Scan(&dictCount); err != nil {
		t.Fatalf("count dict: %v", err)
	}
	if dictCount != 0 {
		t.Errorf("dict rows = %d, want 0 after rollback", dictCount)
	}
}

// ---------------------------------------------------------------------------
// [negative] Ack against an input that is not 'processing' returns an error
// (guards the queue invariant).
// ---------------------------------------------------------------------------
func TestAckInputNotProcessing(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	insertRun(t, ctx, st.Pool, "run-A")

	b := bus.New(st.Pool)
	// Enqueued but never claimed → still 'queued', not 'processing'.
	input, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "system:schedule", ToRef: "agent:1", Content: "input"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	tx, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = bus.Ack(ctx, tx, bus.AckParams{InputID: input.ID, RunID: "run-A"})
	if err == nil {
		t.Fatal("ack on a non-processing input: got nil error, want an error")
	}
}

// ---------------------------------------------------------------------------
// [negative] ClaimNext on a run with no claimable queued rows returns (nil,nil).
// ---------------------------------------------------------------------------
func TestClaimNextEmptyReturnsNil(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	insertRun(t, ctx, st.Pool, "run-A")

	b := bus.New(st.Pool)
	got, err := b.ClaimNext(ctx, time.Minute)
	if err != nil {
		t.Fatalf("claimnext on empty queue: unexpected error %v", err)
	}
	if got != nil {
		t.Errorf("claimnext on empty queue = %+v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// [edge] Two overlapping transactions each ClaimNext the single queued row:
// exactly one wins, the other gets (nil,nil) via FOR UPDATE SKIP LOCKED.
// ---------------------------------------------------------------------------
func TestClaimNextSkipLockedSingleWinner(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	insertRun(t, ctx, st.Pool, "run-A")

	b := bus.New(st.Pool)
	if _, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "system:schedule", ToRef: "agent:1", Content: "only"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	tx1, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	tx2, err := st.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	defer func() { _ = tx2.Rollback(ctx) }()

	b1 := bus.New(tx1)
	b2 := bus.New(tx2)

	// tx1 claims first and holds the row lock until it commits.
	m1, err := b1.ClaimNext(ctx, time.Minute)
	if err != nil {
		t.Fatalf("tx1 claimnext: %v", err)
	}
	// tx2 must skip the locked row and get nothing.
	m2, err := b2.ClaimNext(ctx, time.Minute)
	if err != nil {
		t.Fatalf("tx2 claimnext: %v", err)
	}

	if m1 == nil {
		t.Fatal("tx1 got nil, want the single queued row")
	}
	if m2 != nil {
		t.Errorf("tx2 got %+v, want nil (SKIP LOCKED single-winner)", m2)
	}

	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit tx1: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit tx2: %v", err)
	}
}

// ---------------------------------------------------------------------------
// [edge] ReclaimExpired reverts a processing row whose lease is in the past but
// leaves a still-leased processing row untouched.
// ---------------------------------------------------------------------------
func TestReclaimExpiredOnlyPastLeases(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()
	insertRun(t, ctx, st.Pool, "run-A")

	b := bus.New(st.Pool)
	expired, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "system:schedule", ToRef: "agent:1", Content: "expired"})
	if err != nil {
		t.Fatalf("enqueue expired: %v", err)
	}
	live, err := b.Enqueue(ctx, bus.EnqueueParams{RunID: "run-A", FromRef: "system:schedule", ToRef: "agent:1", Content: "live"})
	if err != nil {
		t.Fatalf("enqueue live: %v", err)
	}

	// One processing row with a past lease, one with a future lease.
	if _, err := st.Pool.Exec(ctx, "UPDATE message SET status='processing', lease_until=now() - interval '1 hour' WHERE id=$1", expired.ID); err != nil {
		t.Fatalf("set expired: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, "UPDATE message SET status='processing', lease_until=now() + interval '1 hour' WHERE id=$1", live.ID); err != nil {
		t.Fatalf("set live: %v", err)
	}

	n, err := b.ReclaimExpired(ctx)
	if err != nil {
		t.Fatalf("reclaimexpired: %v", err)
	}
	if n != 1 {
		t.Errorf("reclaimed count = %d, want 1", n)
	}

	var expiredStatus, liveStatus string
	if err := st.Pool.QueryRow(ctx, "SELECT status FROM message WHERE id=$1", expired.ID).Scan(&expiredStatus); err != nil {
		t.Fatalf("read expired: %v", err)
	}
	if expiredStatus != "queued" {
		t.Errorf("expired row status = %q, want queued (reverted)", expiredStatus)
	}
	if err := st.Pool.QueryRow(ctx, "SELECT status FROM message WHERE id=$1", live.ID).Scan(&liveStatus); err != nil {
		t.Fatalf("read live: %v", err)
	}
	if liveStatus != "processing" {
		t.Errorf("live row status = %q, want processing (untouched)", liveStatus)
	}
}

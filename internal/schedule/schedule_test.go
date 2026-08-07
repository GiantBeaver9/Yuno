package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/giantbeaver9/yuno/internal/bus"
	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/giantbeaver9/yuno/internal/testutil"
)

// insertAgent inserts a minimal agent row and returns its id, so
// schedule.agent_id's FK is satisfied.
func insertAgent(t *testing.T, ctx context.Context, q store.Querier) int64 {
	t.Helper()
	var id int64
	err := q.QueryRow(ctx, "INSERT INTO agent (name) VALUES ($1) RETURNING id", "worker").Scan(&id)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	return id
}

// insertRun inserts a minimal run row so message.run_id's FK (via bus.Enqueue)
// is satisfied.
func insertRun(t *testing.T, ctx context.Context, q store.Querier, guid string) {
	t.Helper()
	if _, err := q.Exec(ctx, "INSERT INTO run (guid) VALUES ($1)", guid); err != nil {
		t.Fatalf("insert run %q: %v", guid, err)
	}
}

// insertSchedule inserts a schedule row and returns its id.
func insertSchedule(t *testing.T, ctx context.Context, q store.Querier, agentID int64, cronExpr string, nextRunAt *time.Time, payload string, enabled bool) int64 {
	t.Helper()
	var id int64
	err := q.QueryRow(ctx,
		"INSERT INTO schedule (agent_id, cron_expr, next_run_at, payload, enabled) VALUES ($1,$2,$3,$4,$5) RETURNING id",
		agentID, cronExpr, nextRunAt, payload, enabled).Scan(&id)
	if err != nil {
		t.Fatalf("insert schedule: %v", err)
	}
	return id
}

func timePtr(t time.Time) *time.Time { return &t }

// ---------------------------------------------------------------------------
// [positive] A due, enabled schedule is fired by Tick: it enqueues a wake
// message from "system:schedule" and advances next_run_at to a future time,
// so a second Due(now) no longer returns it.
// ---------------------------------------------------------------------------
func TestTickFiresDueScheduleAndAdvancesNextRunAt(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()

	agentID := insertAgent(t, ctx, st.Pool)
	insertRun(t, ctx, st.Pool, "run-wake-1")

	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	schID := insertSchedule(t, ctx, st.Pool, agentID, "@every 1m", timePtr(past), "run-wake-1", true)

	b := bus.New(st.Pool)
	sched := New(st.Pool, b)
	sched.now = func() time.Time { return now }

	fired, err := sched.Tick(ctx, now)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if fired != 1 {
		t.Fatalf("fired = %d, want 1", fired)
	}

	// A wake message was enqueued from system:schedule into the seeded run.
	var fromRef, toRef, content, runID string
	err = st.Pool.QueryRow(ctx,
		"SELECT from_ref, to_ref, content, run_id FROM message WHERE run_id=$1", "run-wake-1").
		Scan(&fromRef, &toRef, &content, &runID)
	if err != nil {
		t.Fatalf("read wake message: %v", err)
	}
	if fromRef != "system:schedule" {
		t.Errorf("from_ref = %q, want system:schedule", fromRef)
	}
	if runID != "run-wake-1" {
		t.Errorf("run_id = %q, want run-wake-1", runID)
	}
	if content != "run-wake-1" {
		t.Errorf("content = %q, want run-wake-1 (payload)", content)
	}

	// next_run_at advanced strictly past `now`.
	var newNextRunAt time.Time
	if err := st.Pool.QueryRow(ctx, "SELECT next_run_at FROM schedule WHERE id=$1", schID).Scan(&newNextRunAt); err != nil {
		t.Fatalf("read next_run_at: %v", err)
	}
	if !newNextRunAt.After(now) {
		t.Errorf("next_run_at = %v, want strictly after %v", newNextRunAt, now)
	}

	// A second Due(now) no longer returns it (fire-once).
	due, err := sched.Due(ctx, now)
	if err != nil {
		t.Fatalf("due (second): %v", err)
	}
	if len(due) != 0 {
		t.Errorf("due (second) = %d schedules, want 0", len(due))
	}
}

// ---------------------------------------------------------------------------
// [negative] A disabled schedule with a past next_run_at is not due.
// ---------------------------------------------------------------------------
func TestDueExcludesDisabledSchedule(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()

	agentID := insertAgent(t, ctx, st.Pool)
	insertRun(t, ctx, st.Pool, "run-disabled")

	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	insertSchedule(t, ctx, st.Pool, agentID, "@every 1m", timePtr(past), "run-disabled", false)

	b := bus.New(st.Pool)
	sched := New(st.Pool, b)

	due, err := sched.Due(ctx, now)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("due = %d schedules, want 0 (disabled)", len(due))
	}
}

// ---------------------------------------------------------------------------
// [negative] A schedule with next_run_at in the future is not due.
// ---------------------------------------------------------------------------
func TestDueExcludesFutureSchedule(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()

	agentID := insertAgent(t, ctx, st.Pool)
	insertRun(t, ctx, st.Pool, "run-future")

	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	insertSchedule(t, ctx, st.Pool, agentID, "@every 1m", timePtr(future), "run-future", true)

	b := bus.New(st.Pool)
	sched := New(st.Pool, b)

	due, err := sched.Due(ctx, now)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("due = %d schedules, want 0 (future)", len(due))
	}
}

// ---------------------------------------------------------------------------
// [negative] NextRunAfter on an unrecognized expression errors.
// ---------------------------------------------------------------------------
func TestNextRunAfterInvalidExpression(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	if _, err := NextRunAfter("not-a-cron", now); err == nil {
		t.Fatal("NextRunAfter(\"not-a-cron\", now): got nil error, want an error")
	}
}

// ---------------------------------------------------------------------------
// [edge] A schedule with next_run_at exactly == at is due (<= boundary).
// ---------------------------------------------------------------------------
func TestDueBoundaryNextRunAtEqualsNow(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()

	agentID := insertAgent(t, ctx, st.Pool)
	insertRun(t, ctx, st.Pool, "run-boundary")

	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	insertSchedule(t, ctx, st.Pool, agentID, "@every 1m", timePtr(now), "run-boundary", true)

	b := bus.New(st.Pool)
	sched := New(st.Pool, b)

	due, err := sched.Due(ctx, now)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due = %d schedules, want 1 (next_run_at == at is due)", len(due))
	}
}

// ---------------------------------------------------------------------------
// [edge] NextRunAfter computes the correct next time relative to `after` for
// both a fixed-interval expr (every-minute) and a fixed daily-at-HH:MM expr.
// ---------------------------------------------------------------------------
func TestNextRunAfterTableDriven(t *testing.T) {
	cases := []struct {
		name     string
		cronExpr string
		after    time.Time
		want     time.Time
	}{
		{
			name:     "every minute advances by exactly one minute",
			cronExpr: "@every 1m",
			after:    time.Date(2026, 8, 7, 12, 0, 30, 0, time.UTC),
			want:     time.Date(2026, 8, 7, 12, 1, 30, 0, time.UTC),
		},
		{
			name:     "daily before the target hour fires later the same day",
			cronExpr: "@daily 09:00",
			after:    time.Date(2026, 8, 7, 6, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC),
		},
		{
			name:     "daily after the target hour rolls to the next day",
			cronExpr: "@daily 09:00",
			after:    time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
		},
		{
			name:     "daily exactly at the target time is not due again same day (strictly after)",
			cronExpr: "@daily 09:00",
			after:    time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC),
			want:     time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextRunAfter(tc.cronExpr, tc.after)
			if err != nil {
				t.Fatalf("NextRunAfter(%q, %v): unexpected error: %v", tc.cronExpr, tc.after, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("NextRunAfter(%q, %v) = %v, want %v", tc.cronExpr, tc.after, got, tc.want)
			}
			if !got.After(tc.after) {
				t.Errorf("NextRunAfter(%q, %v) = %v, want strictly after %v", tc.cronExpr, tc.after, got, tc.after)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// [edge] A schedule fired once does not double-fire on an immediate second
// Tick at the same `now` (fire-once / next_run_at advanced).
// ---------------------------------------------------------------------------
func TestTickDoesNotDoubleFireOnSecondTickSameNow(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()

	agentID := insertAgent(t, ctx, st.Pool)
	insertRun(t, ctx, st.Pool, "run-nodouble")

	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	insertSchedule(t, ctx, st.Pool, agentID, "@every 1m", timePtr(now), "run-nodouble", true)

	b := bus.New(st.Pool)
	sched := New(st.Pool, b)
	sched.now = func() time.Time { return now }

	first, err := sched.Tick(ctx, now)
	if err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if first != 1 {
		t.Fatalf("first tick fired = %d, want 1", first)
	}

	second, err := sched.Tick(ctx, now)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if second != 0 {
		t.Errorf("second tick (same now) fired = %d, want 0 (fire-once)", second)
	}

	// Exactly one wake message was enqueued, not two.
	var count int
	if err := st.Pool.QueryRow(ctx, "SELECT count(*) FROM message WHERE run_id=$1", "run-nodouble").Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 1 {
		t.Errorf("message count = %d, want 1 (no double-fire)", count)
	}
}

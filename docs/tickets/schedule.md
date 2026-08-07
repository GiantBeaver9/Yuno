# schedule — third message source (ticker → bus wake)

## Unit
schedule

## Package / Owned files
`internal/schedule/*.go`

## Deps
bus, store

## Tier
standard

## Interfaces
```go
package schedule

import (
	"context"
	"time"

	"github.com/giantbeaver9/yuno/internal/bus"
	"github.com/giantbeaver9/yuno/internal/store"
)

// Schedule mirrors a `schedule` row (§16).
type Schedule struct {
	ID        int64
	AgentID   int64
	CronExpr  string
	NextRunAt *time.Time
	Payload   string // opaque; carries the run guid to enqueue the wake into
	Enabled   bool
}

type Scheduler struct {
	/* q store.Querier; bus *bus.Bus; now func() time.Time */
}

func New(q store.Querier, b *bus.Bus) *Scheduler

// Due returns enabled schedules with next_run_at <= at (the cheap read):
//   SELECT ... WHERE enabled AND next_run_at IS NOT NULL AND next_run_at <= $1
func (s *Scheduler) Due(ctx context.Context, at time.Time) ([]Schedule, error)

// Fire enqueues a wake message (from_ref="system:schedule", to_ref="agent:<id>",
// content=payload) onto the bus, then recomputes and persists next_run_at via
// NextRunAfter. The run to enqueue into is derived from the schedule payload.
func (s *Scheduler) Fire(ctx context.Context, sch Schedule) error

// NextRunAfter computes the next fire time strictly after `after` from cronExpr.
// Supports fixed intervals now; back it with robfig/cron for real cron later.
// Invalid expression → error.
func NextRunAfter(cronExpr string, after time.Time) (time.Time, error)

// Tick runs one pass: Due(at) → Fire each. Returns the number fired.
func (s *Scheduler) Tick(ctx context.Context, at time.Time) (int, error)

// Run does an immediate first pass (catch-up, PORT.md gotcha ③) then ticks every
// 60s until ctx is done.
func (s *Scheduler) Run(ctx context.Context)
```

## Accept
- `Due(at)` returns exactly the `enabled` schedules whose `next_run_at <= at`; disabled or future schedules are excluded.
- `Fire` enqueues a bus message with `from_ref="system:schedule"` and advances `next_run_at` to a strictly-later time (fire-once: the same schedule is not immediately due again).
- `NextRunAfter` returns a time strictly after `after` for a valid expression and errors on an invalid one.
- `Run` does an immediate first pass before entering the 60s ticker so a just-due schedule is not skipped at startup.

## Test cases
All DB tests use `testutil.NewDB`. `schedule.agent_id → agent.id`, and `Fire`'s enqueue needs a `run` row (message.run_id FK) — seed an agent and a run whose guid the payload references.
- **[positive]** Seed an enabled schedule with `next_run_at` in the past → `Due(now)` includes it; `Fire` enqueues a message whose `from_ref=="system:schedule"` and updates `next_run_at` to a future value; a second `Due(now)` no longer returns it.
- **[negative]** A schedule with `enabled=false` and a past `next_run_at` → `Due(now)` excludes it; `NextRunAfter("not-a-cron", now)` → error.
- **[edge]** A schedule with `next_run_at` exactly `== now` is due (`<=` boundary); `NextRunAfter` for a fixed interval returns a time computed relative to the passed `after` (fire-once, no double-fire within a tick).

## Salvage
PORT `salvage/pi-server/reference/telegram/reminders.go` (reminders.go:14-63): the 60s ticker + immediate first pass + fire-once guard — swap `Broadcast`→`bus.Enqueue` wake, `MarkReminded`→advancing `next_run_at`. PORT `digest.go:29-50` `nextDailyTime` for the fixed-daily case, and `events.go:204-367` `NextOccurrenceAfter` as the interval primitive (LIFT its tests). Use `robfig/cron` for real cron beyond fixed intervals.

// Package schedule is the third message source (§16): a ticker that polls the
// `schedule` table for due rows and wakes the owning agent by enqueuing a bus
// message, alongside the human (telegram) and agent-to-agent sources.
package schedule

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
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

// Scheduler polls the schedule table and fires due rows onto the bus.
type Scheduler struct {
	q   store.Querier
	bus *bus.Bus
	now func() time.Time
}

// New returns a Scheduler over the given querier and bus.
func New(q store.Querier, b *bus.Bus) *Scheduler {
	return &Scheduler{q: q, bus: b, now: time.Now}
}

// dueSQL is the cheap read: enabled rows whose next_run_at has arrived.
const dueSQL = `
SELECT id, agent_id, cron_expr, next_run_at, payload, enabled
FROM schedule
WHERE enabled AND next_run_at IS NOT NULL AND next_run_at <= $1
ORDER BY id`

// Due returns enabled schedules with next_run_at <= at.
func (s *Scheduler) Due(ctx context.Context, at time.Time) ([]Schedule, error) {
	rows, err := s.q.Query(ctx, dueSQL, at)
	if err != nil {
		return nil, fmt.Errorf("schedule: due query: %w", err)
	}
	defer rows.Close()

	var out []Schedule
	for rows.Next() {
		var sch Schedule
		if err := rows.Scan(&sch.ID, &sch.AgentID, &sch.CronExpr, &sch.NextRunAt, &sch.Payload, &sch.Enabled); err != nil {
			return nil, fmt.Errorf("schedule: due scan: %w", err)
		}
		out = append(out, sch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("schedule: due rows: %w", err)
	}
	return out, nil
}

// Fire enqueues a wake message (from_ref="system:schedule", to_ref="agent:<id>",
// content=payload) onto the bus, then recomputes and persists next_run_at via
// NextRunAfter. The run to enqueue into is derived from the schedule payload.
func (s *Scheduler) Fire(ctx context.Context, sch Schedule) error {
	_, err := s.bus.Enqueue(ctx, bus.EnqueueParams{
		RunID:   sch.Payload,
		FromRef: "system:schedule",
		ToRef:   fmt.Sprintf("agent:%d", sch.AgentID),
		Content: sch.Payload,
	})
	if err != nil {
		return fmt.Errorf("schedule: fire %d: enqueue: %w", sch.ID, err)
	}

	// NOTE: anchor NextRunAfter on the later of the schedule's own next_run_at
	// and the current poll time. Anchoring on next_run_at alone would only
	// advance by one interval, which can still land <= now for a schedule that
	// has drifted stale (missed several ticks); anchoring on now guarantees
	// fire-once regardless of how overdue the row was.
	anchor := s.now()
	if sch.NextRunAt != nil && sch.NextRunAt.After(anchor) {
		anchor = *sch.NextRunAt
	}
	next, err := NextRunAfter(sch.CronExpr, anchor)
	if err != nil {
		return fmt.Errorf("schedule: fire %d: next run: %w", sch.ID, err)
	}

	if _, err := s.q.Exec(ctx, `UPDATE schedule SET next_run_at=$1 WHERE id=$2`, next, sch.ID); err != nil {
		return fmt.Errorf("schedule: fire %d: persist next_run_at: %w", sch.ID, err)
	}
	return nil
}

// NextRunAfter computes the next fire time strictly after `after` from cronExpr.
// Supports fixed intervals now; back it with robfig/cron for real cron later.
// Invalid expression → error.
//
// Two forms are recognized:
//   - "@every <duration>" — a fixed interval, duration parsed by
//     time.ParseDuration (e.g. "@every 1m", "@every 24h").
//   - "@daily HH:MM" — the next occurrence of that time-of-day in after's
//     location, rolling to the next day if HH:MM has already passed (or is
//     exactly `after`, since the result must be strictly after).
func NextRunAfter(cronExpr string, after time.Time) (time.Time, error) {
	switch {
	case strings.HasPrefix(cronExpr, "@every "):
		durStr := strings.TrimPrefix(cronExpr, "@every ")
		d, err := time.ParseDuration(durStr)
		if err != nil {
			return time.Time{}, fmt.Errorf("schedule: invalid @every duration %q: %w", durStr, err)
		}
		if d <= 0 {
			return time.Time{}, fmt.Errorf("schedule: @every duration must be positive, got %q", durStr)
		}
		return after.Add(d), nil

	case strings.HasPrefix(cronExpr, "@daily "):
		hhmm := strings.TrimPrefix(cronExpr, "@daily ")
		hour, min, err := parseHHMM(hhmm)
		if err != nil {
			return time.Time{}, fmt.Errorf("schedule: invalid @daily time %q: %w", hhmm, err)
		}
		next := time.Date(after.Year(), after.Month(), after.Day(), hour, min, 0, 0, after.Location())
		if !next.After(after) {
			next = next.AddDate(0, 0, 1)
		}
		return next, nil

	default:
		return time.Time{}, fmt.Errorf("schedule: unrecognized cron expression %q", cronExpr)
	}
}

// parseHHMM parses "HH:MM" (24-hour) into hour and minute.
func parseHHMM(s string) (int, int, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want HH:MM, got %q", s)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("bad hour %q", parts[0])
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil || min < 0 || min > 59 {
		return 0, 0, fmt.Errorf("bad minute %q", parts[1])
	}
	return hour, min, nil
}

// Tick runs one pass: Due(at) → Fire each. Returns the number fired.
func (s *Scheduler) Tick(ctx context.Context, at time.Time) (int, error) {
	due, err := s.Due(ctx, at)
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, sch := range due {
		if err := s.Fire(ctx, sch); err != nil {
			return fired, err
		}
		fired++
	}
	return fired, nil
}

// Run does an immediate first pass (catch-up, PORT.md gotcha ③) then ticks every
// 60s until ctx is done.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	s.tick(ctx) // immediate first pass so a just-due schedule isn't skipped at startup
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick runs one Tick pass at the current time, logging (not panicking) on
// error so a transient failure doesn't kill the Run loop.
func (s *Scheduler) tick(ctx context.Context) {
	if _, err := s.Tick(ctx, s.now()); err != nil {
		log.Printf("schedule: tick: %v", err)
	}
}

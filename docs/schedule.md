# schedule — the third message source (ticker → bus)

`schedule` turns the `schedule` table into a poller that wakes agents on a
timer, alongside the two other message sources: a human (telegram) and another
agent. Each `schedule` row names an `agent_id`, a `cron_expr`, a `next_run_at`,
and an opaque `payload` that carries the run guid the wake should land in. When
a row comes due, `Scheduler` enqueues a bus message from `"system:schedule"`
into that run and reschedules the row for its next fire.

## Poll model

`Scheduler` is built with `New(q, bus)` over a `store.Querier` (pool or tx) and
a `*bus.Bus`. Three methods compose:

- **`Due(ctx, at)`** — the cheap read: `SELECT ... FROM schedule WHERE enabled
  AND next_run_at IS NOT NULL AND next_run_at <= $1 ORDER BY id`. Disabled rows
  and rows whose `next_run_at` is still in the future are excluded; a row whose
  `next_run_at` exactly equals `at` **is** due (`<=`, not `<`).
- **`Fire(ctx, sch)`** — enqueues one wake message (`from_ref="system:schedule"`,
  `to_ref="agent:<id>"`, `content=payload`, `run_id=payload`) via `bus.Enqueue`,
  then recomputes `next_run_at` with `NextRunAfter` and persists it.
- **`Tick(ctx, at)`** — one poll pass: `Due(at)` then `Fire` each row returned,
  returning the count fired. This is the whole driver, and it takes an explicit
  `at` rather than reading the clock itself, so tests call it directly with a
  controlled timestamp — no sleeping required.

`Run(ctx)` is the thin production wrapper: it does one immediate `Tick` before
entering a `time.NewTicker(60 * time.Second)` loop, so a schedule that's
already due when the process starts isn't skipped waiting for the first tick
(ported from `salvage/pi-server/reference/telegram/reminders.go`, PORT.md
gotcha ③). It logs (not panics) on a `Tick` error so one bad pass doesn't kill
the loop.

## Fire-once

A schedule must not double-fire if `Tick` is called twice at the same `at` (or
if a row was overdue by more than one interval when it finally got polled).
`Fire` anchors `NextRunAfter`'s `after` argument on `max(sch.NextRunAt,
scheduler's current time)` rather than on `sch.NextRunAt` alone — anchoring on
the stale `next_run_at` alone would only ever advance by one interval, which
can still land `<= now` for a schedule that missed several ticks. Anchoring on
"now" guarantees the persisted `next_run_at` is strictly after the poll time
that just fired it, so the very next `Due` call at that same `at` excludes it.

## Cron expressions

`NextRunAfter(cronExpr, after)` returns the next fire time strictly after
`after`, or an error for an unrecognized expression. Two forms are supported
today:

- **`@every <duration>`** — a fixed interval, parsed by `time.ParseDuration`
  (e.g. `@every 1m`, `@every 24h`). Always `after + duration`.
- **`@daily HH:MM`** — the next occurrence of that time-of-day in `after`'s
  location (ported from `digest.go`'s `nextDailyTime`). Rolls to the next day
  if `HH:MM` has already passed for `after`, or is exactly equal to it (the
  result must be *strictly* after).

Both are pure functions of `(cronExpr, after)` — no clock, no DB — which is why
`NextRunAfterTableDriven` in `schedule_test.go` exercises them directly without
touching Postgres.

> **NOTE:** The ticket lists `github.com/robfig/cron/v3` as an available
> dependency ("already in go.mod") for cron expressions beyond fixed
> intervals. It is **not** actually in `go.mod` (only pre-fetched into the
> local module cache), and this unit's hard rules forbid running `go get` /
> `go mod tidy` or editing `go.mod`. The interface doc comment on
> `NextRunAfter` already scopes this unit to "fixed intervals now; back it
> with robfig/cron for real cron later," so this implementation covers that
> scope with stdlib only (`@every` and `@daily`) and leaves swapping in
> `robfig/cron` for a richer expression syntax as follow-up work once the
> dependency is actually added to `go.mod`.

## Testing time without sleeping

`Scheduler` holds an unexported `now func() time.Time` field, defaulting to
`time.Now`. `schedule_test.go` is in package `schedule` (not `schedule_test`)
specifically so tests can override it directly (`sched.now = func() time.Time
{ return fixedNow }`) and get full determinism for the fire-once anchor,
without needing an exported setter. `Tick`'s explicit `at` parameter covers
the `Due` side of determinism on its own; the `now` field is what makes
`Fire`'s `next_run_at` anchor deterministic too.

# guardrails — cost / rate / blocked-tool checks on the write path

The `guardrails` package gates the costly write/spawn path (ADR-24): before a
run is allowed to spend money, call a tool, or fan out another turn, the
caller checks cost, rate, and the tool blocklist here. Every breach comes
back as a typed `HaltReason` so the caller feeds it straight into the
halt→wait→resume machinery (ADR-20: park the run `needs_human`, wait for an
operator, resume) instead of inventing an ad hoc error shape per check.

## HaltReason

```go
type HaltReason struct {
	Kind   string // one of guardrails.HaltCost / HaltRate / HaltBlocked
	Detail string // human-readable explanation for an operator
}
```

`HaltReason` implements `error` (`Error() string` renders `"guardrails: <kind>
halt: <detail>"`), so a caller that only cares "did this halt" can treat it
as a plain error, while a caller that needs to route on the failure mode can
switch on `Kind`.

## Breaches — the pure core

```go
func Breaches(accumulated, add, agentCap, buildCap float64) *HaltReason
```

Given prior spend (`accumulated`), the amount about to be added (`add`), the
per-agent cap, and the build-wide cap, `Breaches` decides whether this write
would push spend over either cap:

- **A cap of `0` means unlimited.** `max_cost` defaults to `0` on the `agent`
  row (see `internal/store/schema.sql`), so an agent with no configured cap
  is never cost-halted by its own limit — only the build cap (from
  `config.BuildMaxCost`) can still stop it.
- **Breach is strict `>`.** Spending exactly up to a cap is allowed;
  `accumulated + add == cap` is not a breach. This matches "you may spend
  your last dollar," not "you may never touch the last dollar."
- Both caps are checked independently; either one being non-zero and
  exceeded returns a `Kind: HaltCost` reason (the agent cap is checked
  first).

`Breaches` takes no DB dependency and is the unit tests exercise directly;
`Guard.CheckCost` is a thin DB-backed wrapper around it.

## Guard

```go
func New(q store.Querier, ag *agents.Store, buildCap float64) *Guard
```

`Guard` bundles what the three checks need: a `store.Querier` to read prior
spend, the `agents.Store` to read per-agent config (`max_cost`, `rate_limit`,
`blocked_tools`), and the build's cost cap (from `config.BuildMaxCost`).

### CheckCost — cost gate

```go
func (g *Guard) CheckCost(ctx context.Context, runID string, agentID int64, addCost float64) (*HaltReason, error)
```

Sums the run's prior spend (`SELECT COALESCE(SUM(cost), 0) FROM message WHERE
run_id = $1`), loads the agent's `max_cost`, and evaluates both against
`Breaches`. A non-nil `*HaltReason` means the caller should park the run
`needs_human`; a non-nil `error` is a DB/lookup failure, distinct from a
guardrail breach.

### Allow — rate gate

```go
func (g *Guard) Allow(agentID int64) bool
```

A per-agent token bucket sized by `agent.rate_limit`, interpreted as **calls
per minute**: the bucket holds `rate_limit` tokens and refills linearly back
to full over a 60-second window. `Allow` reconciles elapsed time since the
bucket's last check, then consumes one token if available, returning `false`
only when the bucket is empty (a rate breach — the caller is responsible for
wrapping this into a `HaltReason{Kind: HaltRate}` if it wants to park, since
`Allow`'s signature is a plain `bool` per the ticket interface).

`Allow` takes no `context.Context` (fixed by the ticket's interface). On the
first call for a given `agentID` it lazily looks up `rate_limit` via
`agents.Store.Get` using `context.Background()` and caches the resulting
bucket (or the absence of one, for unlimited agents) in `Guard.buckets`, so
subsequent calls for the same agent never hit the DB again.

- `rate_limit <= 0` (including the lookup failing) is treated as
  **unlimited** — `Allow` always returns `true`. This is a deliberate
  fail-open: a rate check that can't be evaluated should not itself become
  an outage.
- The bucket clock is `Guard.now` (defaults to `time.Now`), an unexported
  field the package's own white-box tests override directly to simulate
  refill over time without a real `time.Sleep`.

### IsBlocked — tool blocklist gate

```go
func (g *Guard) IsBlocked(ctx context.Context, agentID int64, tool string) (bool, error)
```

Loads the agent and reports whether `tool` is a member of `blocked_tools`.

## Design notes

- **`Kind` values are exported constants** (`HaltCost`, `HaltRate`,
  `HaltBlocked`) even though only `CheckCost` returns a `*HaltReason`
  directly today — `Allow`/`IsBlocked` return a plain `bool`/`(bool, error)`
  per the ticket's fixed interface. Callers that want a uniform `HaltReason`
  for the halt→park path construct one from these constants when `Allow`
  returns `false` or `IsBlocked` returns `true`.
- **Rate unit is "per minute."** The schema stores `rate_limit` as a bare
  `INTEGER` with no unit; this package picks calls-per-minute as the
  concrete interpretation and documents it here since nothing else in the
  repo pins it down yet.
- **Fail-open on `Allow`'s agent lookup**, fail-closed nowhere: `CheckCost`
  and `IsBlocked` both propagate a DB error rather than silently treating a
  broken agent lookup as "allowed," since those two are on the direct
  write/spend path. `Allow` fails open only because its interface has no
  error return to signal a lookup failure through.

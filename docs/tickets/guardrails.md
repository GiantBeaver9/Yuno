# guardrails — gate the costly writes (cost, rate, blocked tools)

## Unit
guardrails

## Package / Owned files
`internal/guardrails/*.go`

## Deps
store, agents

## Tier
standard

## Interfaces
```go
package guardrails

import (
	"context"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/store"
)

// HaltReason is the typed breach; it doubles as an error. Kind ∈ {cost,rate,blocked}.
type HaltReason struct {
	Kind   string
	Detail string
}

func (h HaltReason) Error() string

// Breaches is the pure, table-driven core: given accumulated spend, the amount
// about to be added, the agent cap and the build cap, decide if this write
// breaches. A cap of 0 means "no limit". Breach is strict-greater (spending
// exactly up to the cap is allowed). Returns nil when clear.
func Breaches(accumulated, add, agentCap, buildCap float64) *HaltReason

type Guard struct {
	/* q store.Querier; agents *agents.Store; buildCap float64; buckets map[int64]*bucket */
}

func New(q store.Querier, ag *agents.Store, buildCap float64) *Guard

// CheckCost sums prior cost for the run (SUM(cost) FROM message WHERE run_id=$1),
// adds addCost, and evaluates against the agent's max_cost and the build cap via
// Breaches. Over → returns a *HaltReason (the run should park needs_human).
func (g *Guard) CheckCost(ctx context.Context, runID string, agentID int64, addCost float64) (*HaltReason, error)

// Allow is a per-agent token bucket sized by agent.rate_limit (0 = unlimited).
// Returns false when the bucket is empty (rate breach).
func (g *Guard) Allow(agentID int64) bool

// IsBlocked reports whether tool is in the agent's blocked_tools[].
func (g *Guard) IsBlocked(ctx context.Context, agentID int64, tool string) (bool, error)
```

## Accept
- `Breaches` returns `nil` when `accumulated+add <= cap` for every non-zero cap, and a `*HaltReason{Kind:"cost"}` when it strictly exceeds either the agent cap or the build cap; a cap of `0` is treated as unlimited.
- `CheckCost` accumulates the run's prior `message.cost` and applies `Breaches` against `agent.max_cost` and the build cap.
- `Allow` refills the per-agent bucket over time and returns `false` only when the agent has exhausted its `rate_limit`.
- `IsBlocked` reflects membership in `agent.blocked_tools`.
- Every breach is a typed halt reason the caller turns into `park needs_human` (ADR-20 halt→wait→resume).

## Test cases
Pure `Breaches`/bucket tests need no DB; `CheckCost`/`IsBlocked` use `testutil.NewDB`.
- **[positive]** `Breaches(1.0, 0.5, 5.0, 100.0)` → `nil`; `IsBlocked` for a tool NOT in `blocked_tools` → `false`; `Allow` returns `true` while the bucket has capacity (agent with `rate_limit>0`, seeded via agents.Create).
- **[negative]** `Breaches(4.0, 2.0, 5.0, 100.0)` → `*HaltReason{Kind:"cost"}` (agent cap exceeded); `IsBlocked` for a tool listed in `blocked_tools` → `true`.
- **[edge]** `Breaches(5.0, 0.0, 5.0, 100.0)` (exactly at cap) → `nil` (breach is strict `>`); `Breaches(999, 999, 0, 0)` → `nil` (both caps 0 = unlimited); a token bucket drained to empty returns `false`, then returns `true` again after enough simulated refill time.

## Salvage
Net-new (§15; PORT.md "Net-new" — reads config off the `agent` row via the agents unit). No guardrail precedent in salvage.

// Package guardrails gates the costly write/spawn path (ADR-24): before a
// run is allowed to spend money, call a tool, or fan out another turn, the
// caller checks cost, rate, and the tool blocklist here. Every breach comes
// back as a typed HaltReason so the caller can reuse the halt->park
// machinery (ADR-20: park needs_human, wait, resume) instead of inventing
// its own error shape per guardrail. See docs/guardrails.md.
package guardrails

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/store"
)

// Halt kinds. A HaltReason.Kind is always one of these.
const (
	HaltCost    = "cost"
	HaltRate    = "rate"
	HaltBlocked = "blocked"
)

// HaltReason is the typed breach a guardrail check returns. Kind is one of
// the Halt* constants above; Detail is a human-readable explanation the
// caller can surface to an operator. HaltReason implements error so callers
// that only care "did this halt" can treat it as a plain error, while still
// being able to type-switch/inspect Kind for the halt->park machinery.
type HaltReason struct {
	Kind   string
	Detail string
}

func (h HaltReason) Error() string {
	return fmt.Sprintf("guardrails: %s halt: %s", h.Kind, h.Detail)
}

// Breaches is the pure, table-driven core: given accumulated spend, the
// amount about to be added, the agent cap and the build cap, decide if this
// write breaches. A cap of 0 means "no limit". Breach is strict-greater
// (spending exactly up to the cap is allowed). Returns nil when clear.
func Breaches(accumulated, add, agentCap, buildCap float64) *HaltReason {
	total := accumulated + add
	if agentCap > 0 && total > agentCap {
		return &HaltReason{
			Kind:   HaltCost,
			Detail: fmt.Sprintf("agent cost cap exceeded: %.6f > %.6f", total, agentCap),
		}
	}
	if buildCap > 0 && total > buildCap {
		return &HaltReason{
			Kind:   HaltCost,
			Detail: fmt.Sprintf("build cost cap exceeded: %.6f > %.6f", total, buildCap),
		}
	}
	return nil
}

// bucketRefillWindow is the period over which a bucket refills from empty to
// full capacity. rate_limit is treated as "calls per minute".
const bucketRefillWindow = 60 * time.Second

// bucket is a per-agent token bucket. capacity tokens refill linearly over
// bucketRefillWindow; allow(now) reconciles elapsed time against last before
// deciding whether a token is available.
type bucket struct {
	capacity float64
	tokens   float64
	last     time.Time
}

// newBucket returns a bucket starting full at now.
func newBucket(capacity float64, now time.Time) *bucket {
	return &bucket{capacity: capacity, tokens: capacity, last: now}
}

// allow reconciles refill up to now, then consumes one token if available.
func (b *bucket) allow(now time.Time) bool {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * (b.capacity / bucketRefillWindow.Seconds())
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Guard holds what's needed to gate the write path for one build: the DB
// handle (to read prior spend), the agents store (to read per-agent caps),
// the build-wide cost cap, and the per-agent rate buckets. now is
// overridable in tests to simulate elapsed time without real sleeps.
type Guard struct {
	q        store.Querier
	agents   *agents.Store
	buildCap float64

	mu      sync.Mutex
	buckets map[int64]*bucket
	now     func() time.Time
}

// New builds a Guard for one build's cost cap.
func New(q store.Querier, ag *agents.Store, buildCap float64) *Guard {
	return &Guard{
		q:        q,
		agents:   ag,
		buildCap: buildCap,
		buckets:  make(map[int64]*bucket),
		now:      time.Now,
	}
}

// CheckCost sums prior cost for the run (SUM(cost) FROM message WHERE
// run_id=$1), adds addCost, and evaluates against the agent's max_cost and
// the build cap via Breaches. Over -> returns a *HaltReason (the run should
// park needs_human).
func (g *Guard) CheckCost(ctx context.Context, runID string, agentID int64, addCost float64) (*HaltReason, error) {
	var accumulated float64
	err := g.q.QueryRow(ctx,
		`SELECT COALESCE(SUM(cost), 0) FROM message WHERE run_id = $1`, runID,
	).Scan(&accumulated)
	if err != nil {
		return nil, fmt.Errorf("guardrails: sum prior cost for run %q: %w", runID, err)
	}

	a, err := g.agents.Get(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("guardrails: get agent %d for cost check: %w", agentID, err)
	}

	return Breaches(accumulated, addCost, a.MaxCost, g.buildCap), nil
}

// Allow is a per-agent token bucket sized by agent.rate_limit (0 =
// unlimited). Returns false when the bucket is empty (rate breach). On first
// use for an agentID it lazily looks up rate_limit and seeds the bucket; if
// that lookup fails, Allow fails open (treats the agent as unlimited) rather
// than blocking the caller on a rate check it can't evaluate.
func (g *Guard) Allow(agentID int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	b, seeded := g.buckets[agentID]
	if !seeded {
		limit := 0
		if a, err := g.agents.Get(context.Background(), agentID); err == nil {
			limit = a.RateLimit
		}
		if limit > 0 {
			b = newBucket(float64(limit), g.now())
		}
		// b stays nil when rate_limit <= 0 (or the lookup failed): unlimited,
		// and we cache that so we don't re-query on every call.
		g.buckets[agentID] = b
	}

	if b == nil {
		return true
	}
	return b.allow(g.now())
}

// IsBlocked reports whether tool is in the agent's blocked_tools[].
func (g *Guard) IsBlocked(ctx context.Context, agentID int64, tool string) (bool, error) {
	a, err := g.agents.Get(ctx, agentID)
	if err != nil {
		return false, fmt.Errorf("guardrails: get agent %d for blocked-tool check: %w", agentID, err)
	}
	for _, blocked := range a.BlockedTools {
		if blocked == tool {
			return true, nil
		}
	}
	return false, nil
}

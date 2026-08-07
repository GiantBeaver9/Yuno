package guardrails

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/secretbox"
	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/giantbeaver9/yuno/internal/testutil"
)

// --- test helpers -----------------------------------------------------

// testBox returns a *secretbox.Box built from an all-zero test key, matching
// internal/agents/agents_test.go's convention.
func testBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.NewFromHex(strings.Repeat("0", 64))
	if err != nil {
		t.Fatalf("secretbox.NewFromHex: %v", err)
	}
	return box
}

// createAgent seeds an agent row with the given cost/rate/blocked knobs and
// returns its id.
func createAgent(t *testing.T, ag *agents.Store, maxCost float64, rateLimit int, blocked []string) int64 {
	t.Helper()
	a, err := ag.Create(context.Background(), agents.CreateParams{
		Name:         "guardrail-test-agent",
		Provider:     "openai",
		Model:        "gpt-5",
		MaxCost:      maxCost,
		RateLimit:    rateLimit,
		BlockedTools: blocked,
	})
	if err != nil {
		t.Fatalf("agents.Create: %v", err)
	}
	return a.ID
}

// seedRun inserts a bare `run` row so `message` FK inserts succeed.
func seedRun(t *testing.T, st *store.Store, guid string) {
	t.Helper()
	_, err := st.Pool.Exec(context.Background(), `INSERT INTO run (guid) VALUES ($1)`, guid)
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

// seedMessageCost inserts one message row carrying the given cost against runID.
func seedMessageCost(t *testing.T, st *store.Store, runID string, seq int64, cost float64) {
	t.Helper()
	_, err := st.Pool.Exec(context.Background(),
		`INSERT INTO message (run_id, seq, from_ref, to_ref, cost) VALUES ($1, $2, 'a', 'b', $3)`,
		runID, seq, cost)
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
}

// --- Breaches (pure) ---------------------------------------------------

func TestBreachesWithinCapsReturnsNil(t *testing.T) {
	// [positive] under both the agent cap and the build cap.
	if got := Breaches(1.0, 0.5, 5.0, 100.0); got != nil {
		t.Fatalf("Breaches: expected nil, got %+v", got)
	}
}

func TestBreachesAgentCapExceededReturnsCostHalt(t *testing.T) {
	// [negative] accumulated+add strictly exceeds the agent cap.
	got := Breaches(4.0, 2.0, 5.0, 100.0)
	if got == nil {
		t.Fatalf("Breaches: expected a cost halt, got nil")
	}
	if got.Kind != "cost" {
		t.Fatalf("Breaches: expected Kind %q, got %q", "cost", got.Kind)
	}
}

func TestBreachesBuildCapExceededReturnsCostHalt(t *testing.T) {
	// [negative] agent cap is generous but the build-wide cap is breached.
	got := Breaches(90.0, 20.0, 1000.0, 100.0)
	if got == nil {
		t.Fatalf("Breaches: expected a cost halt, got nil")
	}
	if got.Kind != "cost" {
		t.Fatalf("Breaches: expected Kind %q, got %q", "cost", got.Kind)
	}
}

func TestBreachesExactlyAtCapIsInclusiveNotABreach(t *testing.T) {
	// [edge] accumulated+add lands exactly on the cap; breach is strict '>'.
	if got := Breaches(5.0, 0.0, 5.0, 100.0); got != nil {
		t.Fatalf("Breaches: expected nil at cap boundary, got %+v", got)
	}
}

func TestBreachesBothCapsZeroIsUnlimited(t *testing.T) {
	// [edge] a cap of 0 means unlimited for both the agent and build cap.
	if got := Breaches(999, 999, 0, 0); got != nil {
		t.Fatalf("Breaches: expected nil with both caps unlimited, got %+v", got)
	}
}

// --- bucket (pure, no DB, no real sleeps) -------------------------------

func TestBucketDrainedThenRefillsAfterElapsedTime(t *testing.T) {
	// [edge] a token bucket drained to empty returns false, then true again
	// after enough simulated refill time -- no real time.Sleep involved.
	start := time.Unix(1_700_000_000, 0)
	b := newBucket(3, start)

	for i := 0; i < 3; i++ {
		if !b.allow(start) {
			t.Fatalf("bucket: expected capacity on call %d", i)
		}
	}
	if b.allow(start) {
		t.Fatalf("bucket: expected drained bucket to deny")
	}

	// capacity 3 refills fully over 60s => simulate a full minute elapsing.
	later := start.Add(60 * time.Second)
	if !b.allow(later) {
		t.Fatalf("bucket: expected refill after elapsed time to allow")
	}
}

// --- Guard.CheckCost (DB) ------------------------------------------------

func TestCheckCostUnderBudgetReturnsNil(t *testing.T) {
	// [positive] prior spend + addCost stays under both the agent and build cap.
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	agentID := createAgent(t, ag, 5.0, 0, nil)
	g := New(st.Pool, ag, 100.0)

	seedRun(t, st, "run-under-budget")
	seedMessageCost(t, st, "run-under-budget", 1, 1.0)

	got, err := g.CheckCost(ctx, "run-under-budget", agentID, 0.5)
	if err != nil {
		t.Fatalf("CheckCost: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("CheckCost: expected nil, got %+v", got)
	}
}

func TestCheckCostOverAgentCapReturnsCostHalt(t *testing.T) {
	// [negative] accumulated run cost pushes this add over the agent's max_cost.
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	agentID := createAgent(t, ag, 5.0, 0, nil)
	g := New(st.Pool, ag, 100.0)

	seedRun(t, st, "run-over-agent-cap")
	seedMessageCost(t, st, "run-over-agent-cap", 1, 4.0)

	got, err := g.CheckCost(ctx, "run-over-agent-cap", agentID, 2.0)
	if err != nil {
		t.Fatalf("CheckCost: unexpected error: %v", err)
	}
	if got == nil {
		t.Fatalf("CheckCost: expected a cost halt, got nil")
	}
	if got.Kind != "cost" {
		t.Fatalf("CheckCost: expected Kind %q, got %q", "cost", got.Kind)
	}
}

func TestCheckCostOverBuildCapReturnsCostHalt(t *testing.T) {
	// [negative] the agent cap is unlimited (0) but the build cap is breached.
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	agentID := createAgent(t, ag, 0, 0, nil)
	g := New(st.Pool, ag, 10.0)

	seedRun(t, st, "run-over-build-cap")
	seedMessageCost(t, st, "run-over-build-cap", 1, 9.0)

	got, err := g.CheckCost(ctx, "run-over-build-cap", agentID, 5.0)
	if err != nil {
		t.Fatalf("CheckCost: unexpected error: %v", err)
	}
	if got == nil {
		t.Fatalf("CheckCost: expected a cost halt, got nil")
	}
	if got.Kind != "cost" {
		t.Fatalf("CheckCost: expected Kind %q, got %q", "cost", got.Kind)
	}
}

func TestCheckCostAgentMaxCostZeroIsUnlimitedButBuildCapStillApplies(t *testing.T) {
	// [edge] agent.max_cost == 0 means "unlimited" for the agent, not "zero
	// budget" -- but the build cap is independent and still enforced.
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	agentID := createAgent(t, ag, 0, 0, nil)
	g := New(st.Pool, ag, 100.0)

	seedRun(t, st, "run-agent-unlimited")
	seedMessageCost(t, st, "run-agent-unlimited", 1, 500.0)

	got, err := g.CheckCost(ctx, "run-agent-unlimited", agentID, 1.0)
	if err != nil {
		t.Fatalf("CheckCost: unexpected error: %v", err)
	}
	if got == nil {
		t.Fatalf("CheckCost: expected a cost halt from the build cap, got nil")
	}
	if got.Kind != "cost" {
		t.Fatalf("CheckCost: expected Kind %q, got %q", "cost", got.Kind)
	}
}

// --- Guard.Allow (DB + injected clock) -----------------------------------

func TestAllowReturnsTrueWhileBucketHasCapacity(t *testing.T) {
	// [positive] a freshly seeded agent with rate_limit > 0 allows calls.
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	agentID := createAgent(t, ag, 0, 5, nil)
	g := New(st.Pool, ag, 100.0)

	if !g.Allow(agentID) {
		t.Fatalf("Allow: expected true while the bucket has capacity")
	}
}

func TestAllowRateLimitZeroIsUnlimited(t *testing.T) {
	// [edge] agent.rate_limit == 0 means unlimited: Allow never denies.
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	agentID := createAgent(t, ag, 0, 0, nil)
	g := New(st.Pool, ag, 100.0)

	for i := 0; i < 50; i++ {
		if !g.Allow(agentID) {
			t.Fatalf("Allow: expected unlimited agent (rate_limit=0) to never deny (call %d)", i)
		}
	}
}

func TestAllowExhaustedBucketDeniesThenRefillsAfterElapsedTime(t *testing.T) {
	// [edge] the rate bucket exhausts and denies, then allows again once the
	// injected clock advances far enough to refill -- no real sleeps.
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	agentID := createAgent(t, ag, 0, 2, nil)
	g := New(st.Pool, ag, 100.0)

	start := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return start }

	if !g.Allow(agentID) {
		t.Fatalf("Allow: expected capacity on call 1")
	}
	if !g.Allow(agentID) {
		t.Fatalf("Allow: expected capacity on call 2")
	}
	if g.Allow(agentID) {
		t.Fatalf("Allow: expected the exhausted bucket to deny")
	}

	later := start.Add(60 * time.Second)
	g.now = func() time.Time { return later }
	if !g.Allow(agentID) {
		t.Fatalf("Allow: expected the bucket to refill after elapsed time")
	}
}

// --- Guard.IsBlocked (DB) -------------------------------------------------

func TestIsBlockedFalseForAllowedTool(t *testing.T) {
	// [positive] the tool is not in the agent's blocked_tools.
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	agentID := createAgent(t, ag, 0, 0, []string{"rm"})
	g := New(st.Pool, ag, 100.0)

	blocked, err := g.IsBlocked(ctx, agentID, "bash")
	if err != nil {
		t.Fatalf("IsBlocked: unexpected error: %v", err)
	}
	if blocked {
		t.Fatalf("IsBlocked: expected false for an allowed tool")
	}
}

func TestIsBlockedTrueForBlockedTool(t *testing.T) {
	// [negative] the tool is listed in the agent's blocked_tools.
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	agentID := createAgent(t, ag, 0, 0, []string{"rm", "curl"})
	g := New(st.Pool, ag, 100.0)

	blocked, err := g.IsBlocked(ctx, agentID, "rm")
	if err != nil {
		t.Fatalf("IsBlocked: unexpected error: %v", err)
	}
	if !blocked {
		t.Fatalf("IsBlocked: expected true for a blocked tool")
	}
}

// --- HaltReason.Error() ---------------------------------------------------

func TestHaltReasonErrorSatisfiesErrorInterface(t *testing.T) {
	// [positive] HaltReason can be used wherever an error is expected.
	var err error = HaltReason{Kind: "cost", Detail: "over budget"}
	if !strings.Contains(err.Error(), "over budget") {
		t.Fatalf("HaltReason.Error(): expected message to contain Detail, got %q", err.Error())
	}
	if errors.Is(err, err) == false {
		// sanity: comparing to itself must hold (identity via errors.Is default behavior)
		t.Fatalf("HaltReason: expected errors.Is comparison against itself to hold")
	}
}

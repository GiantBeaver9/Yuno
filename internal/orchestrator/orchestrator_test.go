package orchestrator_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/bus"
	"github.com/giantbeaver9/yuno/internal/orchestrator"
	"github.com/giantbeaver9/yuno/internal/runner"
	"github.com/giantbeaver9/yuno/internal/secretbox"
	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/giantbeaver9/yuno/internal/testutil"
	"github.com/giantbeaver9/yuno/internal/workflow"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type harness struct {
	st       *store.Store
	bus      *bus.Bus
	wf       *workflow.Store
	ag       *agents.Store
	coderID  int64
	reviewID int64
	wfID     int64
	runGuid  string
}

// newHarness wires the real bus/workflow/agents stores over an isolated schema
// and returns a harness the individual tests seed a workflow into.
func newHarness(t *testing.T) *harness {
	t.Helper()
	st := testutil.NewDB(t)
	ctx := context.Background()

	ag := agents.New(st.Pool, secretbox.New([32]byte{}))
	coder, err := ag.Create(ctx, agents.CreateParams{Name: "coder", Provider: "anthropic", Model: "m", RecipePath: "r", Prompt: "code it"})
	if err != nil {
		t.Fatalf("create coder: %v", err)
	}
	reviewer, err := ag.Create(ctx, agents.CreateParams{Name: "reviewer", Provider: "anthropic", Model: "m", RecipePath: "r", Prompt: "review it"})
	if err != nil {
		t.Fatalf("create reviewer: %v", err)
	}

	return &harness{
		st:       st,
		bus:      bus.New(st.Pool),
		wf:       workflow.New(st.Pool),
		ag:       ag,
		coderID:  coder.ID,
		reviewID: reviewer.ID,
	}
}

// seedRun inserts a run bound to workflow wfID with the given iteration cap.
func (h *harness) seedRun(t *testing.T, maxIterations int) {
	t.Helper()
	h.runGuid = fmt.Sprintf("run-%d", time.Now().UnixNano())
	_, err := h.st.Pool.Exec(context.Background(),
		"INSERT INTO run (guid, workflow_id, status, max_iterations) VALUES ($1, $2, 'running', $3)",
		h.runGuid, h.wfID, maxIterations)
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

// enqueueEntry queues the first message addressed to the coder (the workflow entry).
func (h *harness) enqueueEntry(t *testing.T) {
	t.Helper()
	_, err := h.bus.Enqueue(context.Background(), bus.EnqueueParams{
		RunID:   h.runGuid,
		FromRef: "system:schedule",
		ToRef:   fmt.Sprintf("agent:%d", h.coderID),
		Content: "please implement the ticket",
	})
	if err != nil {
		t.Fatalf("enqueue entry: %v", err)
	}
}

func (h *harness) runStatus(t *testing.T) string {
	t.Helper()
	var s string
	if err := h.st.Pool.QueryRow(context.Background(),
		"SELECT status FROM run WHERE guid = $1", h.runGuid).Scan(&s); err != nil {
		t.Fatalf("read run status: %v", err)
	}
	return s
}

func (h *harness) messages(t *testing.T) []bus.Message {
	t.Helper()
	rows, err := h.st.Pool.Query(context.Background(),
		"SELECT id, run_id, seq, from_ref, to_ref, content, decision, summary, status, lease_until, tokens, cost, created_at FROM message WHERE run_id = $1 ORDER BY seq",
		h.runGuid)
	if err != nil {
		t.Fatalf("query messages: %v", err)
	}
	defer rows.Close()
	var out []bus.Message
	for rows.Next() {
		var m bus.Message
		if err := rows.Scan(&m.ID, &m.RunID, &m.Seq, &m.FromRef, &m.ToRef, &m.Content,
			&m.Decision, &m.Summary, &m.Status, &m.LeaseUntil, &m.Tokens, &m.Cost, &m.CreatedAt); err != nil {
			t.Fatalf("scan message: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func (h *harness) stackCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.st.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM stack WHERE run_id = $1", h.runGuid).Scan(&n); err != nil {
		t.Fatalf("count stack: %v", err)
	}
	return n
}

// drain loops Step until it reports no work, guarding against an infinite loop.
func drain(t *testing.T, o *orchestrator.Orchestrator) int {
	t.Helper()
	ctx := context.Background()
	steps := 0
	for {
		processed, err := o.Step(ctx)
		if err != nil {
			t.Fatalf("Step: %v", err)
		}
		if !processed {
			return steps
		}
		steps++
		if steps > 100 {
			t.Fatalf("Step never drained after %d iterations — runaway loop", steps)
		}
	}
}

func agentRef(id int64) string { return fmt.Sprintf("agent:%d", id) }

// ---------------------------------------------------------------------------
// [positive] the money shot: a coder->reviewer linear workflow runs end to end.
// coder returns `complete` -> routes to reviewer; reviewer returns `approve`
// -> no edge -> terminal. The two agents pass a message between them and the
// run reaches a conclusion (done).
// ---------------------------------------------------------------------------
func TestMoneyShot_LinearWorkflowReachesDone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	w, err := h.wf.CreateWorkflow(ctx, "linear", false)
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	h.wfID = w.ID
	mustNode(t, h.wf, workflow.Node{WorkflowID: w.ID, NodeKey: "coder", AgentID: h.coderID, IsEntry: true})
	mustNode(t, h.wf, workflow.Node{WorkflowID: w.ID, NodeKey: "reviewer", AgentID: h.reviewID})
	mustEdge(t, h.wf, workflow.Edge{WorkflowID: w.ID, FromNode: "coder", ToNode: "reviewer", OnDecision: "complete"})
	// reviewer/approve has NO edge -> terminal.

	h.seedRun(t, 10)
	h.enqueueEntry(t)

	fake := &runner.FakeRunner{Results: []runner.TurnResult{
		{Decision: "complete", Summary: "implemented"},
		{Decision: "approve", Summary: "looks good"},
	}}
	o := orchestrator.New(h.st.Pool, h.bus, h.wf, h.ag, fake, time.Minute)

	if got := drain(t, o); got != 2 {
		t.Fatalf("expected 2 processed steps (coder then reviewer), got %d", got)
	}

	if s := h.runStatus(t); s != "done" {
		t.Errorf("run status = %q, want done", s)
	}

	msgs := h.messages(t)
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages (entry->coder, coder->reviewer), got %d: %+v", len(msgs), msgs)
	}
	// First: the entry addressed to the coder.
	if msgs[0].ToRef != agentRef(h.coderID) {
		t.Errorf("msg[0].to_ref = %q, want %q", msgs[0].ToRef, agentRef(h.coderID))
	}
	// Second: a message flowed FROM the coder TO the reviewer, carrying the verdict.
	if msgs[1].FromRef != agentRef(h.coderID) {
		t.Errorf("msg[1].from_ref = %q, want %q (coder handed off)", msgs[1].FromRef, agentRef(h.coderID))
	}
	if msgs[1].ToRef != agentRef(h.reviewID) {
		t.Errorf("msg[1].to_ref = %q, want %q (reviewer received it)", msgs[1].ToRef, agentRef(h.reviewID))
	}
	if msgs[1].Decision != "complete" {
		t.Errorf("msg[1].decision = %q, want complete (coder verdict carried)", msgs[1].Decision)
	}
	// Every claimed input was acked done — nothing left processing/queued.
	for i, m := range msgs {
		if m.Status != "done" {
			t.Errorf("msg[%d] status = %q, want done", i, m.Status)
		}
	}
	// One stack breadcrumb per turn.
	if n := h.stackCount(t); n != 2 {
		t.Errorf("stack breadcrumbs = %d, want 2 (one per turn)", n)
	}
}

// ---------------------------------------------------------------------------
// [negative] loop guard (ADR-20 halt->park): a reject loop reviewer<->coder
// with a FakeRunner that always rejects and max_iterations=2 must park the run
// needs_human at the cap and stop enqueuing — no infinite loop, bounded messages.
// ---------------------------------------------------------------------------
func TestLoopGuard_RejectLoopParksNeedsHuman(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	w, err := h.wf.CreateWorkflow(ctx, "reject-loop", false)
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	h.wfID = w.ID
	mustNode(t, h.wf, workflow.Node{WorkflowID: w.ID, NodeKey: "coder", AgentID: h.coderID, IsEntry: true})
	mustNode(t, h.wf, workflow.Node{WorkflowID: w.ID, NodeKey: "reviewer", AgentID: h.reviewID})
	mustEdge(t, h.wf, workflow.Edge{WorkflowID: w.ID, FromNode: "coder", ToNode: "reviewer", OnDecision: "reject"})
	mustEdge(t, h.wf, workflow.Edge{WorkflowID: w.ID, FromNode: "reviewer", ToNode: "coder", OnDecision: "reject"})

	h.seedRun(t, 2)
	h.enqueueEntry(t)

	// Always reject — the workflow would loop forever without the guard.
	rejects := make([]runner.TurnResult, 10)
	for i := range rejects {
		rejects[i] = runner.TurnResult{Decision: "reject", Summary: "nope"}
	}
	fake := &runner.FakeRunner{Results: rejects}
	o := orchestrator.New(h.st.Pool, h.bus, h.wf, h.ag, fake, time.Minute)

	drain(t, o)

	if s := h.runStatus(t); s != "needs_human" {
		t.Errorf("run status = %q, want needs_human (loop guard should park)", s)
	}
	// Bounded: entry + two hand-offs = 3, never an unbounded flood.
	msgs := h.messages(t)
	if len(msgs) > 4 {
		t.Fatalf("loop guard failed to bound the queue: %d messages", len(msgs))
	}
	// No message is left claimable — the parked input is acked, nothing re-loops.
	for i, m := range msgs {
		if m.Status == "queued" {
			t.Errorf("msg[%d] still queued after park — guard should stop enqueuing", i)
		}
	}
}

// ---------------------------------------------------------------------------
// [edge] an empty queue is a no-op: Step returns (false, nil).
// ---------------------------------------------------------------------------
func TestStep_EmptyQueueReturnsFalse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	w, err := h.wf.CreateWorkflow(ctx, "empty", false)
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	h.wfID = w.ID
	mustNode(t, h.wf, workflow.Node{WorkflowID: w.ID, NodeKey: "coder", AgentID: h.coderID, IsEntry: true})
	h.seedRun(t, 10)
	// nothing enqueued.

	fake := &runner.FakeRunner{Results: []runner.TurnResult{{Decision: "complete"}}}
	o := orchestrator.New(h.st.Pool, h.bus, h.wf, h.ag, fake, time.Minute)

	processed, err := o.Step(ctx)
	if err != nil {
		t.Fatalf("Step on empty queue: %v", err)
	}
	if processed {
		t.Errorf("Step on empty queue = true, want false")
	}
}

// ---------------------------------------------------------------------------
// [edge] a terminal decision with no matching edge ends the run `done` without
// emitting a phantom output message (the input is still acked, not lost).
// ---------------------------------------------------------------------------
func TestStep_TerminalNoEdgeEndsRunDone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	w, err := h.wf.CreateWorkflow(ctx, "single", false)
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	h.wfID = w.ID
	// single coder node, no edges at all -> any decision is terminal.
	mustNode(t, h.wf, workflow.Node{WorkflowID: w.ID, NodeKey: "coder", AgentID: h.coderID, IsEntry: true})

	h.seedRun(t, 10)
	h.enqueueEntry(t)

	fake := &runner.FakeRunner{Results: []runner.TurnResult{{Decision: "complete", Summary: "done and dusted"}}}
	o := orchestrator.New(h.st.Pool, h.bus, h.wf, h.ag, fake, time.Minute)

	if got := drain(t, o); got != 1 {
		t.Fatalf("expected exactly 1 processed step, got %d", got)
	}

	if s := h.runStatus(t); s != "done" {
		t.Errorf("run status = %q, want done (terminal decision)", s)
	}
	msgs := h.messages(t)
	if len(msgs) != 1 {
		t.Fatalf("terminal decision emitted a phantom message: got %d messages, want 1", len(msgs))
	}
	if msgs[0].Status != "done" {
		t.Errorf("terminal input status = %q, want done (acked, not lost)", msgs[0].Status)
	}
	if n := h.stackCount(t); n != 1 {
		t.Errorf("stack breadcrumbs = %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustNode(t *testing.T, wf *workflow.Store, n workflow.Node) {
	t.Helper()
	if err := wf.AddNode(context.Background(), n); err != nil {
		t.Fatalf("add node %q: %v", n.NodeKey, err)
	}
}

func mustEdge(t *testing.T, wf *workflow.Store, e workflow.Edge) {
	t.Helper()
	if err := wf.AddEdge(context.Background(), e); err != nil {
		t.Fatalf("add edge %s-[%s]->%s: %v", e.FromNode, e.OnDecision, e.ToNode, err)
	}
}

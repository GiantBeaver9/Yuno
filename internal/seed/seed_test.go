package seed_test

import (
	"context"
	"strings"
	"testing"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/secretbox"
	"github.com/giantbeaver9/yuno/internal/seed"
	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/giantbeaver9/yuno/internal/testutil"
	"github.com/giantbeaver9/yuno/internal/workflow"
)

// testBox returns a *secretbox.Box built from an all-zero test key. Fine for
// tests; production keys come from config. Mirrors agents_test.go's helper.
func testBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.NewFromHex(strings.Repeat("0", 64))
	if err != nil {
		t.Fatalf("secretbox.NewFromHex: %v", err)
	}
	return box
}

// counts is a snapshot of the row counts Seed's idempotency guard must hold
// steady across a repeated call.
type counts struct {
	workflows int // is_template = true
	agents    int
	edges     int
}

func countRows(t *testing.T, ctx context.Context, st *store.Store) counts {
	t.Helper()
	var c counts
	if err := st.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM workflow WHERE is_template = true`).Scan(&c.workflows); err != nil {
		t.Fatalf("count workflows: %v", err)
	}
	if err := st.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM agent`).Scan(&c.agents); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if err := st.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM edge`).Scan(&c.edges); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	return c
}

// firstTemplateWorkflowID returns the id of a seeded template workflow, used
// by tests to drill into its nodes/edges via the workflow.Store API.
func firstTemplateWorkflowID(t *testing.T, ctx context.Context, st *store.Store) int64 {
	t.Helper()
	var id int64
	err := st.Pool.QueryRow(ctx, `SELECT id FROM workflow WHERE is_template = true ORDER BY id LIMIT 1`).Scan(&id)
	if err != nil {
		t.Fatalf("find template workflow: %v", err)
	}
	return id
}

// TestSeedCreatesTemplates is the [positive] case: Seed on an empty DB
// creates 2 is_template workflows, a coder and reviewer agent, and nodes +
// edges for at least one template — including the reject-loop edge.
func TestSeedCreatesTemplates(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	wf := workflow.New(st.Pool)

	if err := seed.Seed(ctx, ag, wf); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	c := countRows(t, ctx, st)
	if c.workflows != 2 {
		t.Fatalf("template workflow count = %d, want 2", c.workflows)
	}

	all, err := ag.List(ctx)
	if err != nil {
		t.Fatalf("List agents: %v", err)
	}
	var hasCoder, hasReviewer bool
	for _, a := range all {
		for _, r := range a.Roles {
			if r == "coder" {
				hasCoder = true
			}
			if r == "reviewer" {
				hasReviewer = true
			}
		}
	}
	if !hasCoder {
		t.Errorf("no seeded agent with role %q", "coder")
	}
	if !hasReviewer {
		t.Errorf("no seeded agent with role %q", "reviewer")
	}

	wfID := firstTemplateWorkflowID(t, ctx, st)
	nodes, err := wf.Nodes(ctx, wfID)
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) == 0 {
		t.Errorf("template workflow %d has no nodes", wfID)
	}
	edges, err := wf.Edges(ctx, wfID)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	if len(edges) == 0 {
		t.Errorf("template workflow %d has no edges", wfID)
	}

	to, err := wf.Route(ctx, wfID, "reviewer", "reject")
	if err != nil {
		t.Fatalf("Route reviewer/reject: %v", err)
	}
	if to != "coder" {
		t.Errorf("reviewer --reject--> %q, want %q", to, "coder")
	}
}

// TestSeedIsIdempotent is the [negative] case: calling Seed twice does not
// duplicate rows — the count guard fires on the second call.
func TestSeedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	wf := workflow.New(st.Pool)

	if err := seed.Seed(ctx, ag, wf); err != nil {
		t.Fatalf("Seed (1st): %v", err)
	}
	before := countRows(t, ctx, st)

	if err := seed.Seed(ctx, ag, wf); err != nil {
		t.Fatalf("Seed (2nd): %v", err)
	}
	after := countRows(t, ctx, st)

	if before != after {
		t.Errorf("counts changed after 2nd Seed call: before=%+v after=%+v", before, after)
	}
}

// TestSeedEntryNodeAndRejectEdge is the [edge] case: the coder node is the
// entry node, the reject edge specifically resolves reviewer -> coder, and
// the approve path routes forward to a different node.
func TestSeedEntryNodeAndRejectEdge(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	wf := workflow.New(st.Pool)

	if err := seed.Seed(ctx, ag, wf); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	wfID := firstTemplateWorkflowID(t, ctx, st)

	entry, err := wf.EntryNode(ctx, wfID)
	if err != nil {
		t.Fatalf("EntryNode: %v", err)
	}
	if entry.NodeKey != "coder" || !entry.IsEntry {
		t.Errorf("entry node = %+v, want NodeKey=coder IsEntry=true", entry)
	}

	// The reject edge specifically, queried straight off the edge table as
	// the ticket's accept criteria phrases it, cross-checked via Route.
	var toNode string
	err = st.Pool.QueryRow(ctx,
		`SELECT to_node FROM edge WHERE workflow_id = $1 AND on_decision = 'reject'`, wfID,
	).Scan(&toNode)
	if err != nil {
		t.Fatalf("query reject edge: %v", err)
	}
	if toNode != "coder" {
		t.Errorf("reject edge to_node = %q, want %q", toNode, "coder")
	}
	routed, err := wf.Route(ctx, wfID, "reviewer", "reject")
	if err != nil {
		t.Fatalf("Route reviewer/reject: %v", err)
	}
	if routed != "coder" {
		t.Errorf("Route(reviewer, reject) = %q, want %q", routed, "coder")
	}

	approveTo, err := wf.Route(ctx, wfID, "reviewer", "approve")
	if err != nil {
		t.Fatalf("Route reviewer/approve: %v", err)
	}
	if approveTo == "" || approveTo == "reviewer" {
		t.Errorf("approve path routed to %q, want a forward target", approveTo)
	}

	// Partially-seeded DB: templates already present, calling Seed again
	// must not add a second copy of the entry node's workflow.
	if err := seed.Seed(ctx, ag, wf); err != nil {
		t.Fatalf("Seed (re-run on already-seeded DB): %v", err)
	}
	var wfCount int
	if err := st.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM workflow WHERE is_template = true`).Scan(&wfCount); err != nil {
		t.Fatalf("count workflows: %v", err)
	}
	if wfCount != 2 {
		t.Errorf("template workflow count after re-seed = %d, want 2 (not re-seeded)", wfCount)
	}
}

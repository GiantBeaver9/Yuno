package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/giantbeaver9/yuno/internal/testutil"
	"github.com/giantbeaver9/yuno/internal/workflow"
)

// agentID inserts a minimal agent row (raw SQL, out of unit scope) and
// returns its id, satisfying node.agent_id's FK.
func agentID(t *testing.T, ctx context.Context, st *store.Store, name string) int64 {
	t.Helper()
	var id int64
	err := st.Pool.QueryRow(ctx, "INSERT INTO agent (name) VALUES ($1) RETURNING id", name).Scan(&id)
	if err != nil {
		t.Fatalf("insert agent %q: %v", name, err)
	}
	return id
}

func TestWorkflowRoutingHappyPath(t *testing.T) {
	// [positive] full lifecycle: create workflow, nodes (coder is_entry),
	// edges coder->reviewer on complete and reviewer->coder on reject;
	// Route resolves both edges and EntryNode finds the entry node.
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := workflow.New(st.Pool)

	coderAgent := agentID(t, ctx, st, "coder-agent")
	reviewerAgent := agentID(t, ctx, st, "reviewer-agent")

	wf, err := s.CreateWorkflow(ctx, "build-review", false)
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if wf.ID == 0 {
		t.Fatalf("CreateWorkflow: expected non-zero id")
	}
	if wf.Name != "build-review" || wf.IsTemplate != false {
		t.Fatalf("CreateWorkflow: got %+v", wf)
	}

	got, err := s.GetWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if got != wf {
		t.Fatalf("GetWorkflow: got %+v, want %+v", got, wf)
	}

	if err := s.AddNode(ctx, workflow.Node{WorkflowID: wf.ID, NodeKey: "coder", AgentID: coderAgent, IsEntry: true}); err != nil {
		t.Fatalf("AddNode(coder): %v", err)
	}
	if err := s.AddNode(ctx, workflow.Node{WorkflowID: wf.ID, NodeKey: "reviewer", AgentID: reviewerAgent, IsEntry: false}); err != nil {
		t.Fatalf("AddNode(reviewer): %v", err)
	}

	if err := s.AddEdge(ctx, workflow.Edge{WorkflowID: wf.ID, FromNode: "coder", ToNode: "reviewer", OnDecision: "complete"}); err != nil {
		t.Fatalf("AddEdge(coder->reviewer): %v", err)
	}
	if err := s.AddEdge(ctx, workflow.Edge{WorkflowID: wf.ID, FromNode: "reviewer", ToNode: "coder", OnDecision: "reject"}); err != nil {
		t.Fatalf("AddEdge(reviewer->coder): %v", err)
	}

	toNode, err := s.Route(ctx, wf.ID, "reviewer", "reject")
	if err != nil {
		t.Fatalf("Route(reviewer,reject): %v", err)
	}
	if toNode != "coder" {
		t.Fatalf("Route(reviewer,reject) = %q, want %q", toNode, "coder")
	}

	toNode, err = s.Route(ctx, wf.ID, "coder", "complete")
	if err != nil {
		t.Fatalf("Route(coder,complete): %v", err)
	}
	if toNode != "reviewer" {
		t.Fatalf("Route(coder,complete) = %q, want %q", toNode, "reviewer")
	}

	entry, err := s.EntryNode(ctx, wf.ID)
	if err != nil {
		t.Fatalf("EntryNode: %v", err)
	}
	if entry.NodeKey != "coder" {
		t.Fatalf("EntryNode.NodeKey = %q, want %q", entry.NodeKey, "coder")
	}
	if !entry.IsEntry {
		t.Fatalf("EntryNode.IsEntry = false, want true")
	}
}

func TestNodesAndEdgesListing(t *testing.T) {
	// [positive] Nodes() and Edges() return everything inserted for the
	// workflow.
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := workflow.New(st.Pool)

	a1 := agentID(t, ctx, st, "a1")
	a2 := agentID(t, ctx, st, "a2")

	wf, err := s.CreateWorkflow(ctx, "listing-wf", true)
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wf.ID, NodeKey: "coder", AgentID: a1, IsEntry: true})
	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wf.ID, NodeKey: "reviewer", AgentID: a2, IsEntry: false})
	mustAddEdge(t, ctx, s, workflow.Edge{WorkflowID: wf.ID, FromNode: "coder", ToNode: "reviewer", OnDecision: "complete"})
	mustAddEdge(t, ctx, s, workflow.Edge{WorkflowID: wf.ID, FromNode: "reviewer", ToNode: "coder", OnDecision: "reject"})

	nodes, err := s.Nodes(ctx, wf.ID)
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("Nodes: got %d nodes, want 2", len(nodes))
	}

	edges, err := s.Edges(ctx, wf.ID)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("Edges: got %d edges, want 2", len(edges))
	}
}

func TestNoCrossWorkflowLeakage(t *testing.T) {
	// [positive] reads scope strictly by workflow_id; a second workflow's
	// nodes/edges/entry never leak into the first's results.
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := workflow.New(st.Pool)

	a1 := agentID(t, ctx, st, "a1")
	a2 := agentID(t, ctx, st, "a2")

	wfA, err := s.CreateWorkflow(ctx, "wf-a", false)
	if err != nil {
		t.Fatalf("CreateWorkflow(A): %v", err)
	}
	wfB, err := s.CreateWorkflow(ctx, "wf-b", false)
	if err != nil {
		t.Fatalf("CreateWorkflow(B): %v", err)
	}

	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wfA.ID, NodeKey: "coder", AgentID: a1, IsEntry: true})
	mustAddEdge(t, ctx, s, workflow.Edge{WorkflowID: wfA.ID, FromNode: "coder", ToNode: "coder", OnDecision: "complete"})

	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wfB.ID, NodeKey: "deployer", AgentID: a2, IsEntry: true})
	mustAddEdge(t, ctx, s, workflow.Edge{WorkflowID: wfB.ID, FromNode: "deployer", ToNode: "deployer", OnDecision: "approve"})

	nodesA, err := s.Nodes(ctx, wfA.ID)
	if err != nil {
		t.Fatalf("Nodes(A): %v", err)
	}
	if len(nodesA) != 1 || nodesA[0].NodeKey != "coder" {
		t.Fatalf("Nodes(A) leaked: %+v", nodesA)
	}

	edgesA, err := s.Edges(ctx, wfA.ID)
	if err != nil {
		t.Fatalf("Edges(A): %v", err)
	}
	if len(edgesA) != 1 || edgesA[0].FromNode != "coder" {
		t.Fatalf("Edges(A) leaked: %+v", edgesA)
	}

	entryA, err := s.EntryNode(ctx, wfA.ID)
	if err != nil {
		t.Fatalf("EntryNode(A): %v", err)
	}
	if entryA.NodeKey != "coder" {
		t.Fatalf("EntryNode(A) leaked: %+v", entryA)
	}

	// A decision only defined in B must not resolve under A.
	if _, err := s.Route(ctx, wfA.ID, "deployer", "approve"); !errors.Is(err, workflow.ErrNoRoute) {
		t.Fatalf("Route(A, deployer, approve) = %v, want ErrNoRoute", err)
	}
}

func TestRouteNoMatchingEdge(t *testing.T) {
	// [negative] Route with a decision that has no edge row returns
	// ErrNoRoute.
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := workflow.New(st.Pool)

	a1 := agentID(t, ctx, st, "coder-agent")

	wf, err := s.CreateWorkflow(ctx, "no-route-wf", false)
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wf.ID, NodeKey: "coder", AgentID: a1, IsEntry: true})
	mustAddEdge(t, ctx, s, workflow.Edge{WorkflowID: wf.ID, FromNode: "coder", ToNode: "reviewer", OnDecision: "complete"})

	_, err = s.Route(ctx, wf.ID, "coder", "reject")
	if !errors.Is(err, workflow.ErrNoRoute) {
		t.Fatalf("Route(coder,reject) err = %v, want ErrNoRoute", err)
	}
}

func TestAddNodeMissingAgentFK(t *testing.T) {
	// [negative] AddNode with an agent_id that has no agent row fails with
	// an FK violation.
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := workflow.New(st.Pool)

	wf, err := s.CreateWorkflow(ctx, "fk-wf", false)
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	err = s.AddNode(ctx, workflow.Node{WorkflowID: wf.ID, NodeKey: "coder", AgentID: 999999, IsEntry: true})
	if err == nil {
		t.Fatalf("AddNode with nonexistent agent_id: expected error, got nil")
	}
}

func TestAddEdgeDuplicatePK(t *testing.T) {
	// [negative] AddEdge enforces the PK (workflow_id, from_node,
	// on_decision): inserting the same key twice fails.
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := workflow.New(st.Pool)

	a1 := agentID(t, ctx, st, "coder-agent")

	wf, err := s.CreateWorkflow(ctx, "dup-edge-wf", false)
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wf.ID, NodeKey: "coder", AgentID: a1, IsEntry: true})
	mustAddEdge(t, ctx, s, workflow.Edge{WorkflowID: wf.ID, FromNode: "coder", ToNode: "reviewer", OnDecision: "complete"})

	err = s.AddEdge(ctx, workflow.Edge{WorkflowID: wf.ID, FromNode: "coder", ToNode: "someone-else", OnDecision: "complete"})
	if err == nil {
		t.Fatalf("AddEdge duplicate (workflow_id, from_node, on_decision): expected error, got nil")
	}
}

func TestMultipleEdgesSameNodeDifferentDecisions(t *testing.T) {
	// [edge] two edges from the same from_node on different decisions
	// coexist under the PK and each decision routes to its own to_node.
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := workflow.New(st.Pool)

	coderAgent := agentID(t, ctx, st, "coder-agent")
	deployerAgent := agentID(t, ctx, st, "deployer-agent")

	wf, err := s.CreateWorkflow(ctx, "fanout-wf", false)
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wf.ID, NodeKey: "reviewer", AgentID: coderAgent, IsEntry: true})
	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wf.ID, NodeKey: "coder", AgentID: coderAgent, IsEntry: false})
	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wf.ID, NodeKey: "deployer", AgentID: deployerAgent, IsEntry: false})

	mustAddEdge(t, ctx, s, workflow.Edge{WorkflowID: wf.ID, FromNode: "reviewer", ToNode: "coder", OnDecision: "reject"})
	mustAddEdge(t, ctx, s, workflow.Edge{WorkflowID: wf.ID, FromNode: "reviewer", ToNode: "deployer", OnDecision: "approve"})

	toNode, err := s.Route(ctx, wf.ID, "reviewer", "reject")
	if err != nil {
		t.Fatalf("Route(reviewer,reject): %v", err)
	}
	if toNode != "coder" {
		t.Fatalf("Route(reviewer,reject) = %q, want %q", toNode, "coder")
	}

	toNode, err = s.Route(ctx, wf.ID, "reviewer", "approve")
	if err != nil {
		t.Fatalf("Route(reviewer,approve): %v", err)
	}
	if toNode != "deployer" {
		t.Fatalf("Route(reviewer,approve) = %q, want %q", toNode, "deployer")
	}
}

func TestEntryNodeNoEntry(t *testing.T) {
	// [edge] EntryNode on a workflow with no is_entry node returns an
	// error.
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := workflow.New(st.Pool)

	a1 := agentID(t, ctx, st, "coder-agent")

	wf, err := s.CreateWorkflow(ctx, "no-entry-wf", false)
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wf.ID, NodeKey: "coder", AgentID: a1, IsEntry: false})

	_, err = s.EntryNode(ctx, wf.ID)
	if err == nil {
		t.Fatalf("EntryNode with no is_entry node: expected error, got nil")
	}
}

func TestEntryNodeMultipleEntries(t *testing.T) {
	// [edge] EntryNode on a workflow with more than one is_entry node
	// returns an error (ambiguous entry).
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := workflow.New(st.Pool)

	a1 := agentID(t, ctx, st, "coder-agent")
	a2 := agentID(t, ctx, st, "reviewer-agent")

	wf, err := s.CreateWorkflow(ctx, "multi-entry-wf", false)
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wf.ID, NodeKey: "coder", AgentID: a1, IsEntry: true})
	mustAddNode(t, ctx, s, workflow.Node{WorkflowID: wf.ID, NodeKey: "reviewer", AgentID: a2, IsEntry: true})

	_, err = s.EntryNode(ctx, wf.ID)
	if err == nil {
		t.Fatalf("EntryNode with two is_entry nodes: expected error, got nil")
	}
}

func mustAddNode(t *testing.T, ctx context.Context, s *workflow.Store, n workflow.Node) {
	t.Helper()
	if err := s.AddNode(ctx, n); err != nil {
		t.Fatalf("AddNode(%+v): %v", n, err)
	}
}

func mustAddEdge(t *testing.T, ctx context.Context, s *workflow.Store, e workflow.Edge) {
	t.Helper()
	if err := s.AddEdge(ctx, e); err != nil {
		t.Fatalf("AddEdge(%+v): %v", e, err)
	}
}

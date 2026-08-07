// Package workflow stores workflows as data: named node rows (each bound to
// an agent) and edge rows keyed by (workflow_id, from_node, on_decision).
// Route resolves the single outgoing edge for a decision — the orchestrator
// never hardcodes "reviewer routes to coder on reject"; it only knows the
// decision enum (approve | reject | complete) and asks Route what comes next
// (ADR-15). This is the seam that makes the graph configurable instead of
// baked into orchestrator code.
package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/jackc/pgx/v5"
)

// ErrNoRoute is returned by Route when no edge matches (workflow, from, decision).
var ErrNoRoute = errors.New("workflow: no matching edge")

type Workflow struct {
	ID         int64
	Name       string
	IsTemplate bool
}

type Node struct {
	WorkflowID int64
	NodeKey    string // node.node_key
	AgentID    int64  // node.agent_id (→ agent.id)
	IsEntry    bool
}

type Edge struct {
	WorkflowID int64
	FromNode   string // edge.from_node (a node_key)
	ToNode     string
	OnDecision string // approve | reject | complete
}

type Store struct{ q store.Querier }

func New(q store.Querier) *Store {
	return &Store{q: q}
}

func (s *Store) CreateWorkflow(ctx context.Context, name string, isTemplate bool) (Workflow, error) {
	w := Workflow{Name: name, IsTemplate: isTemplate}
	err := s.q.QueryRow(ctx,
		`INSERT INTO workflow (name, is_template) VALUES ($1, $2) RETURNING id`,
		name, isTemplate,
	).Scan(&w.ID)
	if err != nil {
		return Workflow{}, fmt.Errorf("workflow: create workflow: %w", err)
	}
	return w, nil
}

func (s *Store) GetWorkflow(ctx context.Context, id int64) (Workflow, error) {
	var w Workflow
	err := s.q.QueryRow(ctx,
		`SELECT id, name, is_template FROM workflow WHERE id = $1`,
		id,
	).Scan(&w.ID, &w.Name, &w.IsTemplate)
	if err != nil {
		return Workflow{}, fmt.Errorf("workflow: get workflow %d: %w", id, err)
	}
	return w, nil
}

// AddNode inserts a node row. PK (workflow_id, node_key).
func (s *Store) AddNode(ctx context.Context, n Node) error {
	_, err := s.q.Exec(ctx,
		`INSERT INTO node (workflow_id, node_key, agent_id, is_entry) VALUES ($1, $2, $3, $4)`,
		n.WorkflowID, n.NodeKey, n.AgentID, n.IsEntry,
	)
	if err != nil {
		return fmt.Errorf("workflow: add node %q: %w", n.NodeKey, err)
	}
	return nil
}

// AddEdge inserts an edge row. PK (workflow_id, from_node, on_decision).
func (s *Store) AddEdge(ctx context.Context, e Edge) error {
	_, err := s.q.Exec(ctx,
		`INSERT INTO edge (workflow_id, from_node, to_node, on_decision) VALUES ($1, $2, $3, $4)`,
		e.WorkflowID, e.FromNode, e.ToNode, e.OnDecision,
	)
	if err != nil {
		return fmt.Errorf("workflow: add edge %s -[%s]-> %s: %w", e.FromNode, e.OnDecision, e.ToNode, err)
	}
	return nil
}

func (s *Store) Nodes(ctx context.Context, workflowID int64) ([]Node, error) {
	rows, err := s.q.Query(ctx,
		`SELECT workflow_id, node_key, agent_id, is_entry FROM node WHERE workflow_id = $1 ORDER BY node_key`,
		workflowID,
	)
	if err != nil {
		return nil, fmt.Errorf("workflow: list nodes for workflow %d: %w", workflowID, err)
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.WorkflowID, &n.NodeKey, &n.AgentID, &n.IsEntry); err != nil {
			return nil, fmt.Errorf("workflow: scan node: %w", err)
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workflow: list nodes for workflow %d: %w", workflowID, err)
	}
	return nodes, nil
}

func (s *Store) Edges(ctx context.Context, workflowID int64) ([]Edge, error) {
	rows, err := s.q.Query(ctx,
		`SELECT workflow_id, from_node, to_node, on_decision FROM edge WHERE workflow_id = $1 ORDER BY from_node, on_decision`,
		workflowID,
	)
	if err != nil {
		return nil, fmt.Errorf("workflow: list edges for workflow %d: %w", workflowID, err)
	}
	defer rows.Close()

	var edges []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.WorkflowID, &e.FromNode, &e.ToNode, &e.OnDecision); err != nil {
			return nil, fmt.Errorf("workflow: scan edge: %w", err)
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workflow: list edges for workflow %d: %w", workflowID, err)
	}
	return edges, nil
}

// EntryNode returns the node with is_entry=true. Zero or >1 entries → error.
func (s *Store) EntryNode(ctx context.Context, workflowID int64) (Node, error) {
	rows, err := s.q.Query(ctx,
		`SELECT workflow_id, node_key, agent_id, is_entry FROM node WHERE workflow_id = $1 AND is_entry = true`,
		workflowID,
	)
	if err != nil {
		return Node{}, fmt.Errorf("workflow: entry node for workflow %d: %w", workflowID, err)
	}
	defer rows.Close()

	var found []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.WorkflowID, &n.NodeKey, &n.AgentID, &n.IsEntry); err != nil {
			return Node{}, fmt.Errorf("workflow: scan entry node: %w", err)
		}
		found = append(found, n)
	}
	if err := rows.Err(); err != nil {
		return Node{}, fmt.Errorf("workflow: entry node for workflow %d: %w", workflowID, err)
	}

	switch len(found) {
	case 0:
		return Node{}, fmt.Errorf("workflow: no entry node for workflow %d", workflowID)
	case 1:
		return found[0], nil
	default:
		return Node{}, fmt.Errorf("workflow: %d entry nodes for workflow %d, want exactly 1", len(found), workflowID)
	}
}

// Route reads the single edge keyed (workflow_id, from_node, on_decision) and
// returns its to_node. No such edge → ErrNoRoute. The orchestrator calls this
// knowing NOTHING about roles — it routes on the decision enum only (ADR-15).
func (s *Store) Route(ctx context.Context, workflowID int64, fromNode, decision string) (toNode string, err error) {
	err = s.q.QueryRow(ctx,
		`SELECT to_node FROM edge WHERE workflow_id = $1 AND from_node = $2 AND on_decision = $3`,
		workflowID, fromNode, decision,
	).Scan(&toNode)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNoRoute
		}
		return "", fmt.Errorf("workflow: route %s -[%s]->: %w", fromNode, decision, err)
	}
	return toNode, nil
}

// Package seed populates a fresh Yuno database with the built-in workflow
// templates: named workflow graphs (ADR-15, ADR-16 §11) that ship with their
// own coder and reviewer agents so a new deployment has something runnable
// out of the box instead of an empty `workflow` table.
//
// Seed is count-guarded, mirroring the salvage pi-server seed.go pattern
// (salvage/pi-server/reference/db/seed.go:8-53): it checks whether the seed
// agents already exist and, if so, does nothing. Calling Seed on every
// startup is therefore safe — it seeds once and is a no-op forever after.
//
// All persistence goes through the agents and workflow store APIs; this
// package never touches SQL directly.
package seed

import (
	"context"
	"fmt"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/workflow"
)

// coderRole and reviewerRole tag the two seed agents so the count guard (and
// any caller wanting to find them later) can look them up by role rather than
// by a hardcoded name or id.
const (
	coderRole    = "coder"
	reviewerRole = "reviewer"
)

// templateNames are the 2 workflow templates Seed creates. Both share the
// same coder/reviewer agents and the same reject-loop shape described in
// ADR-16 §11 — coder completes into review, reviewer either sends it back to
// the coder (reject) or forward to a terminal deploy step (approve). Shipping
// 2 near-identical templates (rather than 1) gives operators a second row to
// clone and diverge from without touching the original.
var templateNames = []string{"build-review", "quick-review"}

// Seed creates the built-in workflow templates and their agents if they are
// not already present. It is idempotent: a second call is a no-op.
func Seed(ctx context.Context, ag *agents.Store, wf *workflow.Store) error {
	seeded, err := alreadySeeded(ctx, ag)
	if err != nil {
		return fmt.Errorf("seed: check existing: %w", err)
	}
	if seeded {
		return nil
	}

	coder, err := ag.Create(ctx, agents.CreateParams{
		Name:   "coder",
		Prompt: "You write and edit code to satisfy the ticket, then hand off for review.",
		Mode:   "auto",
		Roles:  []string{coderRole},
	})
	if err != nil {
		return fmt.Errorf("seed: create coder agent: %w", err)
	}

	reviewer, err := ag.Create(ctx, agents.CreateParams{
		Name:   "reviewer",
		Prompt: "You review the coder's change and decide: approve or reject with feedback.",
		Mode:   "approval",
		Roles:  []string{reviewerRole},
	})
	if err != nil {
		return fmt.Errorf("seed: create reviewer agent: %w", err)
	}

	for _, name := range templateNames {
		if err := seedTemplate(ctx, wf, name, coder.ID, reviewer.ID); err != nil {
			return fmt.Errorf("seed: template %q: %w", name, err)
		}
	}
	return nil
}

// seedTemplate creates one reject-loop template workflow: coder (entry)
// --complete--> reviewer --reject--> coder, reviewer --approve--> deployer.
// The deployer terminal node is bound to the reviewer agent — this template
// ships no dedicated deploy agent, so the reviewer's agent stands in for the
// terminal step; operators cloning the template can rebind it.
func seedTemplate(ctx context.Context, wf *workflow.Store, name string, coderAgentID, reviewerAgentID int64) error {
	w, err := wf.CreateWorkflow(ctx, name, true)
	if err != nil {
		return fmt.Errorf("create workflow: %w", err)
	}

	nodes := []workflow.Node{
		{WorkflowID: w.ID, NodeKey: "coder", AgentID: coderAgentID, IsEntry: true},
		{WorkflowID: w.ID, NodeKey: "reviewer", AgentID: reviewerAgentID},
		{WorkflowID: w.ID, NodeKey: "deployer", AgentID: reviewerAgentID},
	}
	for _, n := range nodes {
		if err := wf.AddNode(ctx, n); err != nil {
			return fmt.Errorf("add node %q: %w", n.NodeKey, err)
		}
	}

	edges := []workflow.Edge{
		{WorkflowID: w.ID, FromNode: "coder", ToNode: "reviewer", OnDecision: "complete"},
		{WorkflowID: w.ID, FromNode: "reviewer", ToNode: "coder", OnDecision: "reject"},
		{WorkflowID: w.ID, FromNode: "reviewer", ToNode: "deployer", OnDecision: "approve"},
	}
	for _, e := range edges {
		if err := wf.AddEdge(ctx, e); err != nil {
			return fmt.Errorf("add edge %s -[%s]-> %s: %w", e.FromNode, e.OnDecision, e.ToNode, err)
		}
	}
	return nil
}

// alreadySeeded is the count guard: it looks for an existing agent carrying
// the coder role. workflow.Store exposes no "list all workflows" query (by
// design — callers are expected to know the workflow id they want), so the
// guard checks the agents this package itself creates instead of the
// workflow rows; on a fresh DB the two are created together and never
// diverge, since nothing else in this codebase creates agents named "coder"
// tagged with the coder role.
func alreadySeeded(ctx context.Context, ag *agents.Store) (bool, error) {
	all, err := ag.List(ctx)
	if err != nil {
		return false, fmt.Errorf("list agents: %w", err)
	}
	for _, a := range all {
		for _, r := range a.Roles {
			if r == coderRole {
				return true, nil
			}
		}
	}
	return false, nil
}

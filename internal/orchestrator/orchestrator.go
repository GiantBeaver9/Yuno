// Package orchestrator is the worker loop — the graded spine of the platform
// (PRD §6-8, §17; ADR-13/14/15/20). One Step processes exactly one message:
//
//	claim → resolve node → run the turn → route on decision → atomic ack
//
// The claim is a short transaction (ClaimNext leases the oldest queued row via
// FOR UPDATE SKIP LOCKED). The turn is run outside any transaction. The
// acknowledgement is a single transaction that retires the input, enqueues the
// next node's message, pushes a stack breadcrumb and upserts the dict — all via
// bus.Ack, so it commits or not at all. A crash between claim and ack leaves the
// row leased; the lease expires and Bus.ReclaimExpired requeues it, giving
// at-least-once delivery.
//
// The orchestrator knows nothing about roles (ADR-15): it routes on the
// decision enum only and asks workflow.Route what node comes next. Coder-vs-
// reviewer lives entirely in the edges. ADR-20's halt is realised as the loop
// guard: once a run has taken max_iterations turns the next claim parks it
// needs_human instead of running another (paid) turn.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/bus"
	"github.com/giantbeaver9/yuno/internal/runner"
	"github.com/giantbeaver9/yuno/internal/workflow"
)

// idlePause is how long Run sleeps when a Step finds nothing to do.
const idlePause = 100 * time.Millisecond

// reclaimEvery is how often Run sweeps expired leases back onto the queue.
const reclaimEvery = 5 * time.Second

// Orchestrator composes the bus (claim + atomic ack), the workflow graph
// (Route/nodes), the runner (Run) and the agent config store (turn assembly).
type Orchestrator struct {
	Pool      *pgxpool.Pool
	Bus       *bus.Bus
	Workflows *workflow.Store
	Agents    *agents.Store
	Runner    runner.Runner
	Lease     time.Duration
}

// New wires the orchestrator over an existing pool and its subsystem stores.
func New(
	pool *pgxpool.Pool,
	b *bus.Bus,
	wf *workflow.Store,
	ag *agents.Store,
	r runner.Runner,
	lease time.Duration,
) *Orchestrator {
	return &Orchestrator{
		Pool:      pool,
		Bus:       b,
		Workflows: wf,
		Agents:    ag,
		Runner:    r,
		Lease:     lease,
	}
}

// runRow is the slice of the `run` row the loop reasons about.
type runRow struct {
	WorkflowID    int64
	Status        string
	MaxIterations int
	Iterations    int
}

// Step processes at most one message. It returns (false, nil) when the queue
// has nothing claimable, and (true, nil) when a message was processed (whether
// it routed onward, terminated the run, or parked it).
func (o *Orchestrator) Step(ctx context.Context) (bool, error) {
	// 1. Claim the oldest queued row in a short transaction. Committing leaves
	//    the row 'processing' with a lease; a crash before ack lets the lease
	//    expire so ReclaimExpired requeues it (at-least-once).
	msg, err := o.claim(ctx)
	if err != nil {
		return false, err
	}
	if msg == nil {
		return false, nil // nothing to do
	}

	// 2. Load the run and resolve the current node from the message's to_ref.
	run, err := o.loadRun(ctx, msg.RunID)
	if err != nil {
		return false, err
	}
	agentID, err := parseAgentRef(msg.ToRef)
	if err != nil {
		return false, err
	}
	nodes, err := o.Workflows.Nodes(ctx, run.WorkflowID)
	if err != nil {
		return false, err
	}
	nodeKey, err := resolveNode(nodes, agentID)
	if err != nil {
		return false, err
	}

	// 3. Loop guard (ADR-20 halt→park). Once the run has already taken
	//    max_iterations turns, do NOT run another (paid) turn: park it
	//    needs_human and ack the input with no outputs.
	if run.Iterations >= run.MaxIterations {
		return true, o.park(ctx, msg)
	}

	// 4. Assemble the turn from the agent config + message content, and run it.
	agent, err := o.Agents.Get(ctx, agentID)
	if err != nil {
		return false, err
	}
	// Inject the agent's BYO provider key as the env var goose expects, decrypted
	// only here at turn time (never logged). Best-effort: an agent with no stored
	// key falls back to the platform's inherited goose environment.
	var turnEnv []string
	if agent.Provider != "" {
		if key, kerr := o.Agents.GetProviderKey(ctx, agentID, agent.Provider); kerr == nil {
			turnEnv = runner.ProviderKeyEnv(agent.Provider, key)
		}
	}
	// Materialize the recipe from the DB (source of truth) to a temp file for
	// goose, so it survives ephemeral disk where the original recipe_path may be
	// gone (e.g. after a Railway redeploy). Fall back to the on-disk path.
	recipePath := agent.RecipePath
	if agent.Recipe != "" {
		if tmp, terr := writeTempRecipe(agent.Recipe); terr == nil {
			recipePath = tmp
			defer os.Remove(tmp)
		} else {
			log.Printf("orchestrator: temp recipe for agent %d: %v (falling back to %q)", agentID, terr, agent.RecipePath)
		}
	}
	res, err := o.Runner.Run(ctx, runner.TurnRequest{
		Guid:       msg.RunID,
		RecipePath: recipePath,
		Model:      agent.Model,
		Prompt:     agent.Prompt,
		Input:      msg.Content,
		MaxTurns:   run.MaxIterations,
		Env:        turnEnv,
	})
	if err != nil {
		return false, fmt.Errorf("orchestrator: run turn: %w", err)
	}

	// 5. Route on the decision enum. A matching edge yields the next node's
	//    agent; no edge means this decision terminates the run.
	toNode, err := o.Workflows.Route(ctx, run.WorkflowID, nodeKey, res.Decision)
	terminal := false
	if err != nil {
		if errors.Is(err, workflow.ErrNoRoute) {
			terminal = true
		} else {
			return false, err
		}
	}

	var outputs []bus.EnqueueParams
	if !terminal {
		nextAgentID, err := agentForNode(nodes, toNode)
		if err != nil {
			return false, err
		}
		// The hand-off carries the verdict + summary to the next node.
		outputs = []bus.EnqueueParams{{
			RunID:    msg.RunID,
			FromRef:  formatAgentRef(agentID),
			ToRef:    formatAgentRef(nextAgentID),
			Content:  res.Summary,
			Decision: res.Decision,
			Summary:  res.Summary,
			Tokens:   res.Tokens,
			Cost:     res.Cost,
		}}
	}

	// 6. Atomic ack: in ONE transaction, increment iterations, mark the run
	//    done when terminal, and delegate to bus.Ack (retire input + enqueue
	//    outputs + stack breadcrumb + dict upsert). Commit or roll back whole.
	return true, o.ackTurn(ctx, msg, res, terminal, outputs)
}

// claim runs ClaimNext inside its own committed transaction.
func (o *Orchestrator) claim(ctx context.Context) (*bus.Message, error) {
	tx, err := o.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: begin claim tx: %w", err)
	}
	msg, err := bus.New(tx).ClaimNext(ctx, o.Lease)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("orchestrator: claim: %w", err)
	}
	if msg == nil {
		_ = tx.Rollback(ctx)
		return nil, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("orchestrator: commit claim tx: %w", err)
	}
	return msg, nil
}

// park sets the run needs_human and acks the input with no outputs, in one tx.
func (o *Orchestrator) park(ctx context.Context, msg *bus.Message) error {
	return o.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE run SET status = 'needs_human' WHERE guid = $1`, msg.RunID); err != nil {
			return fmt.Errorf("park run: %w", err)
		}
		return bus.Ack(ctx, tx, bus.AckParams{
			InputID:      msg.ID,
			RunID:        msg.RunID,
			Outputs:      nil,
			StackSummary: "loop guard: parked needs_human at max_iterations",
			DictGuid:     msg.RunID,
			DictDetail:   dictDetail("needs_human", "loop guard halt"),
		})
	})
}

// ackTurn increments iterations (marking the run done when terminal) and acks.
func (o *Orchestrator) ackTurn(ctx context.Context, msg *bus.Message, res runner.TurnResult, terminal bool, outputs []bus.EnqueueParams) error {
	return o.inTx(ctx, func(tx pgx.Tx) error {
		status := "running"
		if terminal {
			status = "done"
		}
		if _, err := tx.Exec(ctx,
			`UPDATE run SET iterations = iterations + 1, status = $2 WHERE guid = $1`,
			msg.RunID, status); err != nil {
			return fmt.Errorf("increment iterations: %w", err)
		}
		return bus.Ack(ctx, tx, bus.AckParams{
			InputID:      msg.ID,
			RunID:        msg.RunID,
			Outputs:      outputs,
			StackSummary: res.Summary,
			DictGuid:     msg.RunID,
			DictDetail:   dictDetail(res.Decision, res.Summary),
		})
	})
}

// inTx runs fn inside a transaction, committing on success and rolling back on
// error so the whole acknowledgement is atomic.
func (o *Orchestrator) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := o.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("orchestrator: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("orchestrator: commit tx: %w", err)
	}
	return nil
}

// loadRun reads the run's routing-relevant columns.
func (o *Orchestrator) loadRun(ctx context.Context, guid string) (runRow, error) {
	var r runRow
	err := o.Pool.QueryRow(ctx,
		`SELECT workflow_id, status, max_iterations, iterations FROM run WHERE guid = $1`,
		guid).Scan(&r.WorkflowID, &r.Status, &r.MaxIterations, &r.Iterations)
	if err != nil {
		return runRow{}, fmt.Errorf("orchestrator: load run %q: %w", guid, err)
	}
	return r, nil
}

// writeTempRecipe writes a goose recipe body to a temp .yaml file and returns
// its path. The caller removes it after the turn.
func writeTempRecipe(body string) (string, error) {
	f, err := os.CreateTemp("", "yuno-recipe-*.yaml")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// resolveNode finds the node whose agent_id matches agentID.
//
// NOTE: this assumes one agent per node — true for the seeded linear/reject
// templates (each agent appears in exactly one node), so the match is
// unambiguous. A workflow that binds the same agent to multiple nodes would
// need the node key threaded through the message instead.
func resolveNode(nodes []workflow.Node, agentID int64) (string, error) {
	for _, n := range nodes {
		if n.AgentID == agentID {
			return n.NodeKey, nil
		}
	}
	return "", fmt.Errorf("orchestrator: no node for agent %d in workflow", agentID)
}

// agentForNode returns the agent bound to nodeKey.
func agentForNode(nodes []workflow.Node, nodeKey string) (int64, error) {
	for _, n := range nodes {
		if n.NodeKey == nodeKey {
			return n.AgentID, nil
		}
	}
	return 0, fmt.Errorf("orchestrator: no node %q in workflow", nodeKey)
}

// parseAgentRef extracts the id from a "agent:<id>" ref.
func parseAgentRef(ref string) (int64, error) {
	const prefix = "agent:"
	if !strings.HasPrefix(ref, prefix) {
		return 0, fmt.Errorf("orchestrator: to_ref %q is not an agent ref", ref)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(ref, prefix), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("orchestrator: bad agent ref %q: %w", ref, err)
	}
	return id, nil
}

func formatAgentRef(id int64) string { return "agent:" + strconv.FormatInt(id, 10) }

// dictDetail is the small heavy-payload blob upserted per turn (dict.full_detail).
func dictDetail(decision, summary string) json.RawMessage {
	b, err := json.Marshal(struct {
		Decision string `json:"decision"`
		Summary  string `json:"summary"`
	}{decision, summary})
	if err != nil {
		return nil
	}
	return b
}

// Run loops Step until ctx is done, pausing briefly when idle and periodically
// reclaiming expired leases so a crashed worker's in-flight message is requeued.
func (o *Orchestrator) Run(ctx context.Context) {
	lastReclaim := time.Now()
	for {
		if ctx.Err() != nil {
			return
		}

		if time.Since(lastReclaim) >= reclaimEvery {
			if _, err := o.Bus.ReclaimExpired(ctx); err != nil && ctx.Err() == nil {
				// A reclaim failure is transient; the next sweep retries.
			}
			lastReclaim = time.Now()
		}

		processed, err := o.Step(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Surface the failure (e.g. a goose/runtime or provider error) so
			// operators can see it, then back off and continue draining. The
			// message stays leased and is retried when the lease expires.
			log.Printf("orchestrator: step error: %v", err)
			processed = false
		}
		if !processed {
			select {
			case <-ctx.Done():
				return
			case <-time.After(idlePause):
			}
		}
	}
}

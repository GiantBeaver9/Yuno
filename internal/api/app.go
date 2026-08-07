package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/bus"
	"github.com/giantbeaver9/yuno/internal/factory"
	"github.com/giantbeaver9/yuno/internal/sse"
	"github.com/giantbeaver9/yuno/internal/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// App is the composition root for the HTTP command API. It holds the subsystem
// stores and the SSE handler and registers every REST route. The bus is the
// spine: creating a run and injecting a human message both just write rows that
// the orchestrator worker (running in the background) drains. SSE is the read
// side of the same rows.
type App struct {
	Pool      *pgxpool.Pool
	Agents    *agents.Store
	Workflows *workflow.Store
	Factory   *factory.Factory
	SSE       *sse.Handler
}

// NewApp wires the App over the shared pool and subsystem stores.
func NewApp(pool *pgxpool.Pool, ag *agents.Store, wf *workflow.Store, fac *factory.Factory, stream *sse.Handler) *App {
	return &App{Pool: pool, Agents: ag, Workflows: wf, Factory: fac, SSE: stream}
}

// Routes registers every /api route (including health) and the SSE stream.
func (a *App) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/agents", a.listAgents)
	mux.HandleFunc("POST /api/agents", a.createAgent) // same backend as the Factory MCP tool

	mux.HandleFunc("GET /api/workflows", a.listWorkflows)
	mux.HandleFunc("GET /api/workflows/{id}", a.getWorkflow)

	mux.HandleFunc("GET /api/runs", a.listRuns)
	mux.HandleFunc("POST /api/runs", a.createRun)
	mux.HandleFunc("GET /api/runs/{guid}/messages", a.runMessages)
	mux.HandleFunc("POST /api/runs/{guid}/messages", a.postMessage)
	mux.HandleFunc("POST /api/runs/{guid}/resume", a.resumeRun)

	mux.HandleFunc("POST /api/messages/{id}/approve", a.approveMessage)

	mux.HandleFunc("GET /api/stream", a.SSE.ServeHTTP)
}

// --- agents ---

func (a *App) listAgents(w http.ResponseWriter, r *http.Request) {
	list, err := a.Agents.List(r.Context())
	if err != nil {
		ServerError(w, err)
		return
	}
	if list == nil {
		list = []agents.Agent{}
	}
	WriteJSON(w, http.StatusOK, list)
}

// createAgentRequest is the UI "new agent" form body — the same fields the
// Factory MCP tool passes. Both hit factory.CreateAgent (one backend, two callers).
type createAgentRequest struct {
	Name         string   `json:"name"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	Key          string   `json:"key"`
	Prompt       string   `json:"prompt"`
	Tools        []string `json:"tools"`
	Roles        []string `json:"roles"`
	Guid         string   `json:"guid"`
	Mode         string   `json:"mode"`
	MaxCost      float64  `json:"maxCost"`
	RateLimit    int      `json:"rateLimit"`
	BlockedTools []string `json:"blockedTools"`
}

func (a *App) createAgent(w http.ResponseWriter, r *http.Request) {
	var req createAgentRequest
	if !ReadJSON(w, r, &req) {
		return
	}
	agent, err := a.Factory.CreateAgent(r.Context(), factory.Spec{
		Name:         req.Name,
		Provider:     req.Provider,
		Model:        req.Model,
		Key:          req.Key,
		Prompt:       req.Prompt,
		Tools:        req.Tools,
		Roles:        req.Roles,
		Guid:         req.Guid,
		Mode:         req.Mode,
		MaxCost:      req.MaxCost,
		RateLimit:    req.RateLimit,
		BlockedTools: req.BlockedTools,
	})
	if err != nil {
		// Validation failures (bad provider, path traversal) are client errors.
		BadRequest(w, err.Error())
		return
	}
	WriteJSON(w, http.StatusCreated, agent)
}

// --- workflows ---

type workflowDTO struct {
	ID         int64           `json:"id"`
	Name       string          `json:"name"`
	IsTemplate bool            `json:"isTemplate"`
	Nodes      []workflow.Node `json:"nodes,omitempty"`
	Edges      []workflow.Edge `json:"edges,omitempty"`
}

func (a *App) listWorkflows(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Pool.Query(r.Context(),
		`SELECT id, name, is_template FROM workflow ORDER BY id`)
	if err != nil {
		ServerError(w, err)
		return
	}
	defer rows.Close()
	out := []workflowDTO{}
	for rows.Next() {
		var d workflowDTO
		if err := rows.Scan(&d.ID, &d.Name, &d.IsTemplate); err != nil {
			ServerError(w, err)
			return
		}
		out = append(out, d)
	}
	WriteJSON(w, http.StatusOK, out)
}

func (a *App) getWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := ParseID(w, r)
	if !ok {
		return
	}
	wf, err := a.Workflows.GetWorkflow(r.Context(), id)
	if err != nil {
		WriteJSON(w, http.StatusNotFound, map[string]string{"error": "workflow not found"})
		return
	}
	nodes, err := a.Workflows.Nodes(r.Context(), id)
	if err != nil {
		ServerError(w, err)
		return
	}
	edges, err := a.Workflows.Edges(r.Context(), id)
	if err != nil {
		ServerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, workflowDTO{
		ID: wf.ID, Name: wf.Name, IsTemplate: wf.IsTemplate, Nodes: nodes, Edges: edges,
	})
}

// --- runs ---

type createRunRequest struct {
	WorkflowID    int64  `json:"workflowId"`
	Input         string `json:"input"`
	MaxIterations int    `json:"maxIterations"`
}

func (a *App) createRun(w http.ResponseWriter, r *http.Request) {
	var req createRunRequest
	if !ReadJSON(w, r, &req) {
		return
	}
	if req.WorkflowID == 0 {
		BadRequest(w, "workflowId is required")
		return
	}
	maxIter := req.MaxIterations
	if maxIter <= 0 {
		maxIter = 10
	}
	ctx := r.Context()

	// The run starts at the workflow's entry node.
	entry, err := a.Workflows.EntryNode(ctx, req.WorkflowID)
	if err != nil {
		BadRequest(w, "workflow has no entry node: "+err.Error())
		return
	}

	guid, err := newGuid()
	if err != nil {
		ServerError(w, err)
		return
	}

	if _, err := a.Pool.Exec(ctx,
		`INSERT INTO run (guid, workflow_id, status, max_iterations) VALUES ($1, $2, 'running', $3)`,
		guid, req.WorkflowID, maxIter); err != nil {
		ServerError(w, err)
		return
	}

	// Enqueue the initial message addressed to the entry agent; the orchestrator
	// worker drains it. Everything is a message on the bus.
	if _, err := bus.New(a.Pool).Enqueue(ctx, bus.EnqueueParams{
		RunID:   guid,
		FromRef: "human:api",
		ToRef:   "agent:" + itoa(entry.AgentID),
		Content: req.Input,
	}); err != nil {
		ServerError(w, err)
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{"guid": guid, "status": "running"})
}

type runDTO struct {
	Guid          string `json:"guid"`
	WorkflowID    int64  `json:"workflowId"`
	Status        string `json:"status"`
	Iterations    int    `json:"iterations"`
	MaxIterations int    `json:"maxIterations"`
}

func (a *App) listRuns(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Pool.Query(r.Context(),
		`SELECT guid, workflow_id, status, iterations, max_iterations FROM run ORDER BY created_at DESC`)
	if err != nil {
		ServerError(w, err)
		return
	}
	defer rows.Close()
	out := []runDTO{}
	for rows.Next() {
		var d runDTO
		if err := rows.Scan(&d.Guid, &d.WorkflowID, &d.Status, &d.Iterations, &d.MaxIterations); err != nil {
			ServerError(w, err)
			return
		}
		out = append(out, d)
	}
	WriteJSON(w, http.StatusOK, out)
}

type messageDTO struct {
	ID       int64   `json:"id"`
	Seq      int64   `json:"seq"`
	FromRef  string  `json:"fromRef"`
	ToRef    string  `json:"toRef"`
	Content  string  `json:"content"`
	Decision string  `json:"decision"`
	Summary  string  `json:"summary"`
	Status   string  `json:"status"`
	Tokens   int     `json:"tokens"`
	Cost     float64 `json:"cost"`
}

func (a *App) runMessages(w http.ResponseWriter, r *http.Request) {
	guid := r.PathValue("guid")
	rows, err := a.Pool.Query(r.Context(),
		`SELECT id, seq, from_ref, to_ref, content, decision, summary, status, tokens, cost
		 FROM message WHERE run_id = $1 ORDER BY seq`, guid)
	if err != nil {
		ServerError(w, err)
		return
	}
	defer rows.Close()
	out := []messageDTO{}
	for rows.Next() {
		var d messageDTO
		if err := rows.Scan(&d.ID, &d.Seq, &d.FromRef, &d.ToRef, &d.Content,
			&d.Decision, &d.Summary, &d.Status, &d.Tokens, &d.Cost); err != nil {
			ServerError(w, err)
			return
		}
		out = append(out, d)
	}
	WriteJSON(w, http.StatusOK, out)
}

type postMessageRequest struct {
	ToAgentID int64  `json:"toAgentId"`
	Content   string `json:"content"`
}

// postMessage injects a human message onto an existing run — the human is a peer
// on the bus (same path Telegram uses).
func (a *App) postMessage(w http.ResponseWriter, r *http.Request) {
	guid := r.PathValue("guid")
	var req postMessageRequest
	if !ReadJSON(w, r, &req) {
		return
	}
	if req.ToAgentID == 0 {
		BadRequest(w, "toAgentId is required")
		return
	}
	m, err := bus.New(a.Pool).Enqueue(r.Context(), bus.EnqueueParams{
		RunID:   guid,
		FromRef: "human:api",
		ToRef:   "agent:" + itoa(req.ToAgentID),
		Content: req.Content,
	})
	if err != nil {
		ServerError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"id": m.ID, "seq": m.Seq})
}

// approveMessage clears an approval-mode halt: a parked message flips back to
// queued and the worker resumes (ADR-20: one halt→wait→resume mechanic).
func (a *App) approveMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := ParseID(w, r)
	if !ok {
		return
	}
	tag, err := a.Pool.Exec(r.Context(),
		`UPDATE message SET status = 'queued', lease_until = NULL
		 WHERE id = $1 AND status = 'pending_approval'`, id)
	if err != nil {
		ServerError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		WriteJSON(w, http.StatusConflict, map[string]string{"error": "message not pending approval"})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

// resumeRun lifts a needs_human loop-guard halt: raise the iteration ceiling and
// re-enqueue a wake at the entry node so the run continues (demo-grade resume).
func (a *App) resumeRun(w http.ResponseWriter, r *http.Request) {
	guid := r.PathValue("guid")
	ctx := r.Context()

	var workflowID int64
	if err := a.Pool.QueryRow(ctx,
		`UPDATE run SET status = 'running', max_iterations = max_iterations + 5
		 WHERE guid = $1 RETURNING workflow_id`, guid).Scan(&workflowID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			WriteJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
			return
		}
		ServerError(w, err)
		return
	}
	entry, err := a.Workflows.EntryNode(ctx, workflowID)
	if err != nil {
		ServerError(w, err)
		return
	}
	if _, err := bus.New(a.Pool).Enqueue(ctx, bus.EnqueueParams{
		RunID:   guid,
		FromRef: "human:api",
		ToRef:   "agent:" + itoa(entry.AgentID),
		Content: "resume",
	}); err != nil {
		ServerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "running"})
}

// --- helpers ---

func newGuid() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

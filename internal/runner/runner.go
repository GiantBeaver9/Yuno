// Package runner shells out to a headless goose agent for a single disposable
// turn and parses its trailing verdict from captured stdout.
package runner

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// ErrNoDecision is returned by ParseDecision when stdout carries no valid verdict.
var ErrNoDecision = errors.New("runner: no DECISION line in output")

// TurnRequest is one agent turn (§7). The caller (orchestrator) assembles it.
type TurnRequest struct {
	Guid       string
	RecipePath string   // agent.recipe_path
	Model      string   // agent.model
	Prompt     string   // assembled system/context prompt
	Input      string   // the bus message content driving this turn
	MaxTurns   int      // GOOSE MAX_TURNS ceiling
	WorkDir    string   // workspaces/<build_id>
	Env        []string // extra env (e.g. provider key), never logged
}

// TurnResult is the parsed outcome of a turn.
type TurnResult struct {
	Decision string // approve | reject | complete
	Summary  string
	Output   string // full captured stdout
	Tokens   int
	Cost     float64
}

// Runner is the seam the orchestrator depends on (FakeRunner in tests).
type Runner interface {
	Run(ctx context.Context, req TurnRequest) (TurnResult, error)
}

// GooseRunner shells out to `goose run` headless: GOOSE_MODE=auto, --max-turns,
// per-turn subprocess (disposable), stdout captured and parsed.
type GooseRunner struct {
	GoosePath string // config.GoosePath
}

func NewGooseRunner(goosePath string) *GooseRunner {
	return &GooseRunner{GoosePath: goosePath}
}

func (g *GooseRunner) Run(ctx context.Context, req TurnRequest) (TurnResult, error) {
	goosePath := g.GoosePath
	if goosePath == "" {
		goosePath = "goose"
	}

	args := []string{
		"run",
		"--max-turns", strconv.Itoa(req.MaxTurns),
		"--text", req.Input,
	}
	if req.RecipePath != "" {
		args = append(args, "--recipe", req.RecipePath)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	cmd := exec.CommandContext(ctx, goosePath, args...)
	cmd.Dir = req.WorkDir
	cmd.Env = append(cmd.Env, "GOOSE_MODE=auto")
	cmd.Env = append(cmd.Env, req.Env...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	decision, summary, parseErr := ParseDecision(stdout.String())
	result := TurnResult{
		Decision: decision,
		Summary:  summary,
		Output:   stdout.String(),
	}

	if runErr != nil {
		return result, runErr
	}
	if parseErr != nil {
		return result, parseErr
	}
	return result, nil
}

// ParseDecision is the testable core: it scans captured stdout for the agent's
// trailing verdict. Contract — the agent prints, on their own lines:
//
//	DECISION: <approve|reject|complete>
//	SUMMARY: <short text>
//
// Matching is case-insensitive; the LAST DECISION line wins; SUMMARY is optional
// (defaults to ""). An unknown/absent decision → ErrNoDecision.
func ParseDecision(stdout string) (decision, summary string, err error) {
	const decisionPrefix = "decision:"
	const summaryPrefix = "summary:"

	lines := strings.Split(stdout, "\n")

	lastDecision := ""
	lastSummary := ""
	found := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		switch {
		case strings.HasPrefix(lower, decisionPrefix):
			val := strings.TrimSpace(trimmed[len(decisionPrefix):])
			lastDecision = strings.ToLower(val)
			found = true
		case strings.HasPrefix(lower, summaryPrefix):
			lastSummary = strings.TrimSpace(trimmed[len(summaryPrefix):])
		}
	}

	if !found {
		return "", "", ErrNoDecision
	}

	switch lastDecision {
	case "approve", "reject", "complete":
		return lastDecision, lastSummary, nil
	default:
		return "", "", ErrNoDecision
	}
}

// FakeRunner returns scripted results in order (for orchestrator/unit tests).
type FakeRunner struct {
	Results []TurnResult
	Err     error
	// internal cursor advances each Run call
	cursor int
}

func (f *FakeRunner) Run(ctx context.Context, req TurnRequest) (TurnResult, error) {
	if f.Err != nil {
		return TurnResult{}, f.Err
	}
	if f.cursor >= len(f.Results) {
		return TurnResult{}, errors.New("runner: FakeRunner has no more scripted results")
	}
	res := f.Results[f.cursor]
	f.cursor++
	return res, nil
}

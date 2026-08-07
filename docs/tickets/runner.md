# runner — disposable goose run + decision/summary parsing

## Unit
runner

## Package / Owned files
`internal/runner/*.go`

## Deps
(none — leaf)

## Tier
standard

## Interfaces
```go
package runner

import (
	"context"
	"errors"
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
	Decision string  // approve | reject | complete
	Summary  string
	Output   string  // full captured stdout
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

func NewGooseRunner(goosePath string) *GooseRunner
func (g *GooseRunner) Run(ctx context.Context, req TurnRequest) (TurnResult, error)

// ParseDecision is the testable core: it scans captured stdout for the agent's
// trailing verdict. Contract — the agent prints, on their own lines:
//   DECISION: <approve|reject|complete>
//   SUMMARY: <short text>
// Matching is case-insensitive; the LAST DECISION line wins; SUMMARY is optional
// (defaults to ""). An unknown/absent decision → ErrNoDecision.
func ParseDecision(stdout string) (decision, summary string, err error)

// FakeRunner returns scripted results in order (for orchestrator/unit tests).
type FakeRunner struct {
	Results []TurnResult
	Err     error
	// internal cursor advances each Run call
}

func (f *FakeRunner) Run(ctx context.Context, req TurnRequest) (TurnResult, error)
```

## Accept
- `ParseDecision` extracts a valid `decision ∈ {approve,reject,complete}` and the `summary` from a stdout blob; the last `DECISION:` line wins when several appear.
- Only the three enum values are accepted; any other token → `ErrNoDecision`.
- `FakeRunner` yields its `Results` in sequence and surfaces `Err`; it never shells out.
- `GooseRunner.Run` sets `GOOSE_MODE=auto` and a max-turns bound, captures stdout, and returns a `TurnResult` via `ParseDecision`. (The subprocess itself is not exercised in unit tests — parsing is.)

## Test cases
- **[positive]** `ParseDecision("...work...\nDECISION: approve\nSUMMARY: looks good\n")` → `("approve","looks good",nil)`; `FakeRunner{Results:[{Decision:"complete"}]}` returns that result on the first `Run`.
- **[negative]** `ParseDecision` on output containing `DECISION: maybe` (invalid enum) → `ErrNoDecision`; on output with no `DECISION:` line at all → `ErrNoDecision`.
- **[edge]** Two `DECISION:` lines (`reject` then `approve`) → last one (`approve`) wins; a `DECISION: COMPLETE` in uppercase is accepted (case-insensitive) and normalized to `complete`; missing `SUMMARY` line → summary is `""` with no error.

## Salvage
Net-new (§18 step 1 — de-risk Goose). No goose/subprocess precedent in salvage; the OpenAI/LM-Studio loop in `reference/llm` is SUPERSEDED by Goose.

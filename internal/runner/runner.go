// Package runner shells out to a headless goose agent for a single disposable
// turn and parses its trailing verdict from captured stdout.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// stderrTail returns the last ~600 chars of goose's stderr, trimmed, for error
// context without flooding logs.
func stderrTail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		s = "…" + s[len(s)-600:]
	}
	return s
}

// ErrNoDecision is returned by ParseDecision when stdout carries no valid verdict.
var ErrNoDecision = errors.New("runner: no DECISION line in output")

// VerdictInstructions is the contract every agent must honour so the orchestrator
// can route: end the final message with a DECISION (and optional SUMMARY) line.
// Recipes embed this in their instructions; the no-recipe path injects it via
// --system.
const VerdictInstructions = "When you have finished this turn, end your final " +
	"message with exactly these lines:\nDECISION: <approve|reject|complete>\n" +
	"SUMMARY: <one short sentence>"

// GooseProviderName maps a Yuno provider to goose's provider identifier (used in
// a recipe's settings.goose_provider).
func GooseProviderName(provider string) string {
	switch strings.ToLower(provider) {
	case "gemini", "google":
		return "google"
	case "huggingface", "hf":
		return "huggingface"
	default:
		return strings.ToLower(provider)
	}
}

// ProviderKeyEnv maps a Yuno provider + plaintext key to the environment variable
// goose reads for that provider. Returns nil for an unknown provider or empty key.
func ProviderKeyEnv(provider, key string) []string {
	if key == "" {
		return nil
	}
	switch strings.ToLower(provider) {
	case "gemini", "google":
		return []string{"GOOGLE_API_KEY=" + key}
	case "huggingface", "hf":
		return []string{"HF_TOKEN=" + key}
	default:
		return nil
	}
}

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

	// Headless, disposable turn: no session file. We deliberately do NOT pass
	// --quiet — goose's diagnostics (provider/tool/auth errors) then reach
	// stdout/stderr where operators can see them; ParseDecision scans the whole
	// output for the trailing DECISION line regardless of the surrounding chatter.
	args := []string{
		"run",
		"--no-session",
		"--max-turns", strconv.Itoa(req.MaxTurns),
	}
	if req.RecipePath != "" {
		// A recipe carries the agent's instructions, provider and model; the
		// per-turn input is passed as the `task` parameter (goose forbids
		// --text alongside --recipe). See factory.buildRecipe.
		args = append(args, "--recipe", req.RecipePath, "--params", "task="+req.Input)
	} else {
		// No recipe: drive goose directly with the input and the agent's prompt
		// (plus the verdict contract) as system instructions. Provider/model/key
		// come from the inherited goose environment (GOOSE_PROVIDER/GOOSE_MODEL/…).
		system := req.Prompt
		if system != "" {
			system += "\n\n"
		}
		system += VerdictInstructions
		args = append(args, "--text", req.Input, "--system", system)
	}

	cmd := exec.CommandContext(ctx, goosePath, args...)
	cmd.Dir = req.WorkDir
	// Inherit the process environment (goose needs HOME/PATH for its config and
	// tools), then layer goose settings + the per-agent provider key.
	env := append(os.Environ(), "GOOSE_MODE=auto", "GOOSE_DISABLE_KEYRING=true")
	env = append(env, req.Env...)
	cmd.Env = env

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

	// Surface goose's stderr on failure — that's where provider/tool/auth errors
	// land — so operators can see WHY a turn failed (goose diagnostics go to
	// stderr; --quiet keeps stdout to the model response only).
	if runErr != nil {
		return result, fmt.Errorf("goose run failed: %w; stderr: %s", runErr, stderrTail(stderr.String()))
	}
	if parseErr != nil {
		if s := stderrTail(stderr.String()); s != "" {
			return result, fmt.Errorf("%w; goose stderr: %s", parseErr, s)
		}
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

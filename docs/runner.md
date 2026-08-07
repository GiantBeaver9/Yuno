# runner — disposable goose turns + decision/summary parsing

The `runner` package is the seam between the orchestrator and the `goose` CLI. It runs one agent "turn" per invocation as a disposable, non-interactive subprocess, captures its stdout, and parses the agent's trailing verdict out of that text.

## Disposable-turn model

Each call to `Runner.Run` corresponds to exactly one `TurnRequest`: a fresh `goose run` subprocess is started (headless, `GOOSE_MODE=auto`, bounded by `--max-turns`), it does its work in `req.WorkDir`, prints its output, and exits. There is no persistent session between turns — the orchestrator re-invokes `Run` with a new `TurnRequest` (new prompt/input, same or different `WorkDir`) for the next turn. This keeps each turn independently retryable, cheap to reason about, and free of hidden cross-turn state inside the runner itself; any state that must survive between turns lives in the workspace directory or the orchestrator, not in the `Runner`.

## Output contract goose agents must emit

To hand control back to the orchestrator, the recipe/prompt driving each goose agent MUST make the agent print its verdict as trailing lines of its own stdout, in this exact form:

```
DECISION: <approve|reject|complete>
SUMMARY: <short text>
```

Rules enforced by `ParseDecision`:

- **Matching is case-insensitive.** `DECISION:`, `decision:`, and `Decision:` are all recognized; the value (`approve`/`reject`/`complete`) is normalized to lowercase in the returned `TurnResult`.
- **The LAST `DECISION:` line wins.** If the agent second-guesses itself and prints more than one, only the final one in the output counts.
- **`SUMMARY:` is optional.** If absent, `TurnResult.Summary` is `""` with no error. When present, its value is whatever follows the last `SUMMARY:` marker, whitespace-trimmed.
- **Only the three enum values are accepted**: `approve`, `reject`, `complete`. Anything else (a typo, `maybe`, an empty value) — or no `DECISION:` line at all, including empty stdout — causes `ParseDecision` to return `runner.ErrNoDecision`. Callers should treat this as a turn failure requiring operator/agent attention, not silently retry.

Both markers are matched as a line prefix after trimming surrounding whitespace, so leading/trailing spaces around the marker or its value are tolerated (e.g. `DECISION:   approve   ` parses to `approve`).

## Package shape

- `Runner` — the interface the orchestrator depends on (`Run(ctx, TurnRequest) (TurnResult, error)`).
- `GooseRunner` — the real implementation. Builds an `exec.Command` for the `goose` binary (`GOOSE_MODE=auto` env var, `--max-turns` flag, optional `--recipe`/`--model`, `WorkDir` as the subprocess's working directory, extra `req.Env` appended — never logged), captures stdout, and delegates parsing to `ParseDecision`.
- `ParseDecision(stdout string) (decision, summary string, err error)` — the pure, fully unit-tested core described above. `GooseRunner.Run` is intentionally thin around it since the `goose` binary itself is not available in this environment for integration testing.
- `FakeRunner` — for orchestrator and downstream unit tests. Holds a `Results []TurnResult` slice and an optional `Err`. Each `Run` call returns the next scripted `TurnResult` in order (advancing an internal cursor), or `Err` immediately if set, or an "exhausted" error once `Results` is used up. It never shells out, so tests using it have no dependency on `goose` being installed.

## Using FakeRunner in tests

```go
fr := &runner.FakeRunner{Results: []runner.TurnResult{
    {Decision: "approve", Summary: "looks good"},
    {Decision: "complete", Summary: "done"},
}}

res, err := fr.Run(ctx, runner.TurnRequest{ /* ... */ })
// res.Decision == "approve" on the first call, "complete" on the second.
```

To simulate a failing turn, set `Err` instead of (or in addition to) `Results`; `Run` returns that error immediately without consuming a scripted result.

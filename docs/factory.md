# factory — one endpoint, two callers (PRD §10, ADR-18)

`internal/factory` is the single backend function behind agent creation.
`Factory.CreateAgent(ctx, Spec)` is called by both the UI "new agent" form
and the Factory MCP tool (`create_agent`) — there is exactly one validated
code path, never arbitrary shell, for turning a `Spec` into a running agent.

## Flow

`CreateAgent` does, strictly in this order:

1. **`ValidateSpec`** — a whitelist check, nothing more:
   - `Name` non-empty.
   - `Provider` ∈ `AllowedProviders` (`gemini`, `huggingface`).
   - The recipe path derived from `Name` stays under `agentsDir` (see
     traversal defense below).

   Validation runs entirely **before** any write. A bad spec produces no
   recipe file and no `agent` row — not even a partial one.

2. **Write the recipe file** under `agentsDir`, named from the agent's
   `Name`. The recipe file is the agent's "code": provider/model, prompt,
   tools, guardrails, roles — the same content whichever caller (UI or MCP
   tool) requested it. Writing refuses to silently clobber an existing file:
   if a recipe already exists at that path (a name collision), `CreateAgent`
   errors and nothing is touched.

3. **`agents.Create`** — inserts the `agent` row via `internal/agents`, with
   `recipe_path` set to the file written in step 2.

4. **`agents.SetProviderKey`** — stores the caller-supplied plaintext `Key`
   encrypted at rest via `internal/secretbox`, keyed by `(agent_id,
   provider)`. Encryption happens inside `agents`; `factory` only passes the
   plaintext through — it never persists a key itself.

If step 3 or 4 fails after the recipe file was written, `CreateAgent` makes a
best-effort attempt to remove the orphaned recipe file before returning the
error, so a retry under the same name isn't blocked by leftover state.

## Path-traversal defense

`RecipePath(agentsDir, name)` is the single place a `Name` becomes a
filesystem path, and it is deliberately strict rather than permissive:

- Any path separator in `name` (`/` or `\`) is rejected outright — this alone
  blocks `"../evil"`, `"../../etc/passwd"`, and `"a/b"`, and also blocks
  absolute paths (which necessarily contain a separator).
- `name == "."` or `name == ".."` is rejected.
- As defense in depth, the resolved absolute path is still checked against
  `agentsDir` via `filepath.Rel` — if it ever computed a path that starts
  with `..`, that's rejected too, even though the separator check above
  already makes this unreachable in practice.

Nothing is normalized or "cleaned up" into a safe-looking name — an unsafe
name is refused, not sanitized and silently accepted. This also means two
names differing only by case or spacing (`"Coder"` vs `"coder"`) naturally
map to distinct recipe files, since the raw name (plus a `.yaml` suffix) is
the filename; no lossy normalization is applied that could collide them.

## Whitelisted validation, not arbitrary fields

`ValidateSpec` only enforces the fields called out above (name, provider,
path safety). `Model`, `Mode`, `Prompt`, `Tools`, `Roles`, `MaxCost`,
`RateLimit`, and `BlockedTools` all pass through to `agents.Create` as-is;
`agents` applies its own defaults (e.g. `Mode` defaults to `"auto"` when
empty). This keeps `factory`'s validation surface exactly the whitelist the
ticket specifies — a bigger validation surface than that is out of scope
here and belongs, if ever needed, to the `agents` layer or its schema
constraints.

## Not arbitrary shell

`Factory.CreateAgent` is a validated function call, not a shell-out. Both
callers (UI handler, MCP tool) construct a `Spec` and call the same Go
function — there is no path where either caller can inject a command or
write outside `agentsDir`.

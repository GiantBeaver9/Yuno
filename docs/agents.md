# agents — config CRUD + roles-via-join + encrypted provider keys

The `agents` package is CRUD over three tables: `agent` (the config knobs),
`agent_roles` (a mutable join, ADR-9), and `provider_key` (per-agent BYO
provider keys, encrypted at rest with `secretbox`).

## Model

- **Agent** (`agent` table): `id`, `name`, `provider`, `model`, `recipe_path`,
  `prompt`, `tools` (`TEXT[]`), `mode` (`auto` | `approval`), `max_cost`,
  `rate_limit`, `blocked_tools` (`TEXT[]`), `guid`. These are the run-time
  knobs a workflow node's agent carries: which provider/model to call, the
  recipe and system prompt, the tool allow/block lists, the cost ceiling and
  rate limit, and whether it can act without human approval.
- **Roles** (`agent_roles` table, PK `(agent_id, role)`): an agent can carry
  zero or more free-form role tags (e.g. `"coder"`, `"reviewer"`). Roles are
  mutable and non-unique across agents — the join, not an enum column, is the
  source of truth (ADR-9).
- **Provider key** (`provider_key` table, PK `(agent_id, provider)`): a BYO
  API key for one provider, stored as `enc_key BYTEA`. The plaintext key is
  never a field on `Agent` — it is fetched and decrypted on demand via
  `GetProviderKey`, and never logged.

## Roles: replace, not append

`SetRoles(ctx, agentID, roles)` is delete-then-insert: it wipes the agent's
existing `agent_roles` rows and inserts the deduped set passed in. Calling it
twice with the same set (even with duplicates, e.g. `["coder","coder"]`)
converges to exactly one row per distinct role — no `ON CONFLICT` needed
because the table is cleared first. `Create` uses the same dedup+insert path
for the `Roles` given in `CreateParams`.

`GetRoles` returns an agent's roles sorted by role name. `Get` and `List`
populate `Agent.Roles` via a `LEFT JOIN agent_roles` aggregated with
`array_agg(... ORDER BY role) FILTER (WHERE role IS NOT NULL)`, so an agent
with no roles comes back with `Roles == []string{}` (present, not dropped)
rather than requiring a second round trip.

## Provider keys: encrypted at rest

`SetProviderKey(ctx, agentID, provider, key)` encrypts `key` with the
`*secretbox.Box` the `Store` was constructed with and upserts it:

```sql
INSERT INTO provider_key (agent_id, provider, enc_key) VALUES ($1, $2, $3)
ON CONFLICT (agent_id, provider) DO UPDATE SET enc_key = excluded.enc_key
```

The PK `(agent_id, provider)` means calling `SetProviderKey` again for the
same agent/provider overwrites in place — one row per (agent, provider),
always holding the latest key. The bytes written to `enc_key` are never
byte-equal to the plaintext (fresh random nonce + AES-256-GCM seal each
call); `GetProviderKey` reverses this — load `enc_key`, `box.Decrypt` it,
return the plaintext string. A missing row, a wrong key, or tampered
ciphertext all surface as an error, never a zero-value success or a partial
plaintext.

`Agent`/`CreateParams`/`List`/`Get` never touch `provider_key` — provider
keys are strictly opt-in reads via `GetProviderKey`, keeping them out of the
`GET /agents` response shape.

## The `GET /agents` shape

`List(ctx)` returns every agent with all config knobs, `Guid`, and `Roles`
populated — exactly the shape a `GET /agents` HTTP handler would serialize.
No provider keys are ever included, encrypted or otherwise.

## Errors

`Get` on a nonexistent id, and `GetProviderKey` when no key row exists for
`(agentID, provider)`, both wrap `pgx.ErrNoRows` — callers can
`errors.Is(err, pgx.ErrNoRows)` to distinguish "not found" from other
failures.

## Example

```go
box, _ := secretbox.NewFromHex(hexKey)
s := agents.New(pool, box)

a, err := s.Create(ctx, agents.CreateParams{
    Name:     "coder-agent",
    Provider: "openai",
    Model:    "gpt-5",
    Mode:     "approval", // empty Mode defaults to "auto"
    Roles:    []string{"coder"},
})

_ = s.SetProviderKey(ctx, a.ID, "openai", "sk-...")
key, err := s.GetProviderKey(ctx, a.ID, "openai") // decrypted plaintext

agents, err := s.List(ctx) // GET /agents shape: knobs + roles + guid
```

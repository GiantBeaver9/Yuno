# ghsync — git ops per build, PAT hygiene

The `ghsync` package handles the git-and-GitHub side of syncing a Yuno build to a branch and pull request. It is deliberately split into a pure, fully-tested arg/payload-construction surface and a thin exec seam so the token-hygiene guarantees can be verified without a real git binary or GitHub API.

## Project → repo, build → branch

Each Yuno **project** maps to one GitHub repository. Each **build** for that project gets its own branch:

```
BranchName(buildNumber int) string  // "build/<n>"
```

`BranchName(4)` is `"build/4"`, `BranchName(0)` is `"build/0"`. The branch name is deterministic and requires no I/O.

## PR-per-ticket lifecycle

Every build's changes land in a dedicated pull request rather than being pushed straight to the project's base branch:

1. Clone (or reuse a working copy) and check out `build/<n>` (`CloneArgs`).
2. Commit the build's changes (`CommitArgs`).
3. Push the branch (`PushArgs`, executed by `GitCLI.Push`).
4. Open a PR from `build/<n>` onto the repo's base branch via the GitHub "create PR" API, using the payload built by `PRRequestBody(title, head, base, body)`.

`PRRequestBody` marshals a `PRRequest{Title, Head, Base, Body}` to JSON matching GitHub's create-PR endpoint; all four fields round-trip through `encoding/json`, including titles/bodies with quotes, newlines, and non-ASCII text.

The `Pusher` interface (`Push(ctx, PushRequest) error`) is the network seam: production code uses `GitCLI`, tests can substitute a mock, and neither the interface nor the pure arg builders ever take or return a token — only `PushRequest.Token` carries it, and only as far as the environment of the `git` subprocess.

## PAT handling: decrypted only at git-op time, never logged

The GitHub personal access token (PAT) is stored encrypted at rest as `secret_github.enc_pat`, using `secretbox` (AES-256-GCM). `ghsync` never persists a plaintext PAT:

- `DecryptPAT(box *secretbox.Box, encPAT []byte) (string, error)` decrypts it on demand, right before a git operation needs it. A `nil` box, or ciphertext that fails `secretbox`'s authentication check (wrong key, truncated/tampered bytes), returns an error rather than any partial or wrong plaintext.
- The plaintext PAT is passed to `GitCLI.Push` via `PushRequest.Token` and used to build a **transient** `http.extraheader` credential (`AUTHORIZATION: basic base64(x-access-token:<token>)`), injected purely through the subprocess's environment (`GIT_CONFIG_COUNT` / `GIT_CONFIG_KEY_0` / `GIT_CONFIG_VALUE_0`, supported by git ≥ 2.31). The token is never:
  - written into `CloneArgs`, `CommitArgs`, or `PushArgs` output (none of those functions accept a token at all),
  - embedded in the clone's remote URL or any file under the repo's working copy,
  - written to a git config file on disk, or
  - present in the process's argv (visible via `ps`/process listings).
- If the underlying `git push` fails, any command output is passed through `Redact(token, output)` before being wrapped into the returned error, so a token that leaked into git's stderr (e.g. inside a `*url.Error`-shaped failure message) never reaches a log or an operator's terminal.

## Redact

```go
func Redact(token, s string) string
```

Replaces every occurrence of `token` in `s` with `"***"`. An empty token is treated as "nothing to redact" and returns `s` unchanged — it is **not** a wildcard that strips everything. `Redact` is applied defensively anywhere a token-bearing string (git output, an error's `.Error()` text) might otherwise be logged or surfaced.

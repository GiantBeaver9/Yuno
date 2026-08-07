# ghsync — git ops per build, PAT hygiene

## Unit
ghsync

## Package / Owned files
`internal/ghsync/*.go`

## Deps
secretbox

## Tier
standard

## Interfaces
```go
package ghsync

import (
	"context"

	"github.com/giantbeaver9/yuno/internal/secretbox"
)

// BranchName is the per-build branch: "build/<n>" (§12).
func BranchName(buildNumber int) string

// Redact replaces every occurrence of token in s with "***". Empty token → s
// unchanged (never replace-all of ""). Guards against a token-bearing *url.Error
// leaking into logs (PORT.md gotcha ②).
func Redact(token, s string) string

// DecryptPAT decrypts secret_github.enc_pat only at git-op time. The plaintext
// PAT is never logged and never written into the clone.
func DecryptPAT(box *secretbox.Box, encPAT []byte) (string, error)

// Arg builders — the pure, tested surface. Token is NEVER an argument; it is
// injected via env / a credential helper by the Pusher impl.
func CloneArgs(repoURL, dir, branch string) []string
func CommitArgs(message string) []string
func PushArgs(remote, branch string) []string

// PRRequest is the GitHub "create PR" payload.
type PRRequest struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
}

// PRRequestBody marshals a PRRequest to the GitHub API JSON body.
func PRRequestBody(title, head, base, body string) ([]byte, error)

// Pusher is the network seam so git construction stays testable and the actual
// push is optional/mockable.
type Pusher interface {
	Push(ctx context.Context, req PushRequest) error
}

type PushRequest struct {
	RepoDir string
	Remote  string
	Branch  string
	Token   string // used to build a transient env credential; never persisted/logged
}

// GitCLI shells out to git; injects Token via env (e.g. GIT_ASKPASS / credential
// helper), never via a URL written into the clone.
type GitCLI struct{ GitPath string }

func (g *GitCLI) Push(ctx context.Context, req PushRequest) error
```

## Accept
- `BranchName(n)` == `"build/"+n`.
- `Redact` removes every occurrence of a non-empty token; with an empty token it returns the input verbatim.
- Arg builders produce the correct `git` argv and never embed the token; `PushArgs` output contains no secret (token flows only through `PushRequest.Token` → env at op time).
- `PRRequestBody` produces JSON with `title`/`head`/`base`/`body`.
- `DecryptPAT` returns the plaintext PAT via secretbox; callers keep it out of logs and the clone.

## Test cases
- **[positive]** `BranchName(4)` == `"build/4"`; `PRRequestBody("t","build/4","main","b")` unmarshals back to the same four fields; `CloneArgs(url,dir,"build/4")` contains `dir` and the branch.
- **[negative]** `Redact("ghp_abc","fatal: https://x:ghp_abc@github.com/...")` returns a string not containing `ghp_abc`; assert `PushArgs("origin","build/4")` contains no token substring.
- **[edge]** `Redact("", "anything")` == `"anything"` (empty token is a no-op, not a replace-all); `BranchName(0)` == `"build/0"`.

## Salvage
Net-new (§12; PORT.md "Net-new" #3 — no secret handling in salvage). secretbox for PAT decrypt; the `redact()` concept is the token-redaction pattern flagged in telegram PORT gotcha ② / pi-server PORT.md (bot.go:242-278).

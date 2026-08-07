// Package ghsync handles the git-and-GitHub side of syncing a Yuno build to
// a branch and pull request.
//
// A Yuno "project" maps to a GitHub repository; each "build" for that
// project gets its own branch, named build/<n> (BranchName), and its own
// pull request against the repo's base branch — a PR-per-ticket lifecycle
// where every build's changes land in a reviewable, isolated PR rather than
// being pushed straight to the base branch.
//
// The GitHub personal access token (PAT) that authorizes pushes is stored
// encrypted at rest (secret_github.enc_pat) and is decrypted via DecryptPAT
// only at the moment a git operation needs it. The decrypted token is never
// written into the clone, never placed in a command's argv (see CloneArgs,
// CommitArgs, PushArgs — none of them accept or emit a token), and never
// logged: GitCLI.Push injects it purely through the process environment
// (a transient git http.extraheader credential), and Redact scrubs any
// token substring — including one embedded in a URL inside an error such
// as *url.Error — out of strings before they are ever surfaced.
package ghsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/giantbeaver9/yuno/internal/secretbox"
)

// BranchName is the per-build branch: "build/<n>" (§12).
func BranchName(buildNumber int) string {
	return fmt.Sprintf("build/%d", buildNumber)
}

// Redact replaces every occurrence of token in s with "***". Empty token → s
// unchanged (never replace-all of ""). Guards against a token-bearing *url.Error
// leaking into logs (PORT.md gotcha ②).
func Redact(token, s string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}

// DecryptPAT decrypts secret_github.enc_pat only at git-op time. The plaintext
// PAT is never logged and never written into the clone.
func DecryptPAT(box *secretbox.Box, encPAT []byte) (string, error) {
	if box == nil {
		return "", errors.New("ghsync: nil secretbox")
	}
	plaintext, err := box.Decrypt(encPAT)
	if err != nil {
		return "", fmt.Errorf("ghsync: decrypt PAT: %w", err)
	}
	return string(plaintext), nil
}

// CloneArgs builds the argv for "git clone" without embedding any token.
// Authentication, when needed, flows via env (see GitCLI.Push), never via a
// credential embedded in repoURL here.
func CloneArgs(repoURL, dir, branch string) []string {
	return []string{"clone", "--branch", branch, "--single-branch", repoURL, dir}
}

// CommitArgs builds the argv for "git commit".
func CommitArgs(message string) []string {
	return []string{"commit", "-m", message}
}

// PushArgs builds the argv for "git push" without embedding any token.
func PushArgs(remote, branch string) []string {
	return []string{"push", remote, branch}
}

// PRRequest is the GitHub "create PR" payload.
type PRRequest struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
}

// PRRequestBody marshals a PRRequest to the GitHub API JSON body.
func PRRequestBody(title, head, base, body string) ([]byte, error) {
	return json.Marshal(PRRequest{Title: title, Head: head, Base: base, Body: body})
}

// Pusher is the network seam so git construction stays testable and the actual
// push is optional/mockable.
type Pusher interface {
	Push(ctx context.Context, req PushRequest) error
}

// PushRequest carries what GitCLI.Push needs to perform an authenticated push.
type PushRequest struct {
	RepoDir string
	Remote  string
	Branch  string
	Token   string // used to build a transient env credential; never persisted/logged
}

// GitCLI shells out to git; injects Token via env (e.g. GIT_ASKPASS / credential
// helper), never via a URL written into the clone.
type GitCLI struct{ GitPath string }

// Push runs "git push" for req, injecting req.Token via a transient
// http.extraheader credential passed through the process environment
// (GIT_CONFIG_COUNT / GIT_CONFIG_KEY_0 / GIT_CONFIG_VALUE_0 — supported by
// git >= 2.31). The token never appears in argv, is never written to a git
// config file on disk, and is redacted from any error output before it is
// returned.
func (g *GitCLI) Push(ctx context.Context, req PushRequest) error {
	if req.Token == "" {
		return errors.New("ghsync: empty token")
	}
	if req.RepoDir == "" {
		return errors.New("ghsync: empty repo dir")
	}
	if req.Remote == "" || req.Branch == "" {
		return errors.New("ghsync: empty remote or branch")
	}

	gitPath := g.GitPath
	if gitPath == "" {
		gitPath = "git"
	}

	args := PushArgs(req.Remote, req.Branch)
	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Dir = req.RepoDir

	authHeader := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+req.Token))
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0="+authHeader,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ghsync: git push failed: %s: %w", Redact(req.Token, string(out)), err)
	}
	return nil
}

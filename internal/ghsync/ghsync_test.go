package ghsync

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/giantbeaver9/yuno/internal/secretbox"
)

// ---------- BranchName ----------

func TestBranchNamePositive(t *testing.T) {
	// [positive] BranchName(4) == "build/4"
	got := BranchName(4)
	want := "build/4"
	if got != want {
		t.Fatalf("BranchName(4) = %q, want %q", got, want)
	}
}

func TestBranchNameZero(t *testing.T) {
	// [edge] BranchName(0) == "build/0"
	got := BranchName(0)
	want := "build/0"
	if got != want {
		t.Fatalf("BranchName(0) = %q, want %q", got, want)
	}
}

func TestBranchNameLarge(t *testing.T) {
	// [edge] branch name for a large build number
	got := BranchName(1234567890)
	want := "build/1234567890"
	if got != want {
		t.Fatalf("BranchName(1234567890) = %q, want %q", got, want)
	}
}

// ---------- Redact ----------

func TestRedactRemovesToken(t *testing.T) {
	// [negative] Redact removes the token substring from a string containing it.
	token := "ghp_abc"
	s := "fatal: https://x:ghp_abc@github.com/owner/repo.git failed"
	got := Redact(token, s)
	if strings.Contains(got, token) {
		t.Fatalf("Redact output still contains token: %q", got)
	}
}

func TestRedactEmptyTokenNoOp(t *testing.T) {
	// [edge] Redact("", "anything") == "anything" (empty token is a no-op, not replace-all)
	got := Redact("", "anything")
	want := "anything"
	if got != want {
		t.Fatalf("Redact(\"\", %q) = %q, want %q", want, got, want)
	}
}

func TestRedactMultipleOccurrences(t *testing.T) {
	// [edge] Redact when the token appears multiple times / as part of a URL.
	token := "ghp_secretTOKEN123"
	s := "clone url: https://ghp_secretTOKEN123@github.com/o/r.git push url: https://ghp_secretTOKEN123@github.com/o/r.git"
	got := Redact(token, s)
	if strings.Contains(got, token) {
		t.Fatalf("Redact output still contains token: %q", got)
	}
	if !strings.Contains(got, "***") {
		t.Fatalf("Redact output should contain redaction marker, got: %q", got)
	}
}

func TestRedactURLError(t *testing.T) {
	// [negative] guards against a token-bearing *url.Error style message leaking.
	token := "ghp_deadbeef"
	s := `Get "https://ghp_deadbeef@github.com/owner/repo.git/info/refs": dial tcp: connection refused`
	got := Redact(token, s)
	if strings.Contains(got, token) {
		t.Fatalf("Redact output still contains token: %q", got)
	}
}

// ---------- PRRequestBody ----------

func TestPRRequestBodyValidJSON(t *testing.T) {
	// [positive] PRRequestBody produces valid JSON with title/head/base/body; round-trips.
	data, err := PRRequestBody("t", "build/4", "main", "b")
	if err != nil {
		t.Fatalf("PRRequestBody returned error: %v", err)
	}
	var pr PRRequest
	if err := json.Unmarshal(data, &pr); err != nil {
		t.Fatalf("PRRequestBody output did not unmarshal: %v", err)
	}
	if pr.Title != "t" || pr.Head != "build/4" || pr.Base != "main" || pr.Body != "b" {
		t.Fatalf("PRRequestBody round-trip mismatch: %+v", pr)
	}
}

func TestPRRequestBodySpecialChars(t *testing.T) {
	// [edge] PR body with special chars is properly JSON-escaped.
	title := `Fix "quotes" & <html> issues`
	body := "line1\nline2\t\"quoted\" & unicode: éè \\backslash\\"
	data, err := PRRequestBody(title, "build/7", "main", body)
	if err != nil {
		t.Fatalf("PRRequestBody returned error: %v", err)
	}
	var pr PRRequest
	if err := json.Unmarshal(data, &pr); err != nil {
		t.Fatalf("PRRequestBody output did not unmarshal: %v", err)
	}
	if pr.Title != title {
		t.Fatalf("title mismatch: got %q, want %q", pr.Title, title)
	}
	if pr.Body != body {
		t.Fatalf("body mismatch: got %q, want %q", pr.Body, body)
	}
	// Sanity: it must actually be valid JSON overall.
	var generic map[string]interface{}
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("PRRequestBody output is not valid JSON: %v", err)
	}
}

func TestPRRequestBodyEmptyFields(t *testing.T) {
	// [negative] empty title/head/base still produces valid JSON (no error from empty inputs).
	data, err := PRRequestBody("", "", "", "")
	if err != nil {
		t.Fatalf("PRRequestBody with empty fields returned error: %v", err)
	}
	var pr PRRequest
	if err := json.Unmarshal(data, &pr); err != nil {
		t.Fatalf("PRRequestBody output did not unmarshal: %v", err)
	}
}

// ---------- CloneArgs / CommitArgs / PushArgs ----------

func TestCloneArgsPositive(t *testing.T) {
	// [positive] CloneArgs(url,dir,"build/4") contains dir and the branch.
	args := CloneArgs("https://github.com/owner/repo.git", "/tmp/work/repo", "build/4")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "/tmp/work/repo") {
		t.Fatalf("CloneArgs missing dir: %v", args)
	}
	if !strings.Contains(joined, "build/4") {
		t.Fatalf("CloneArgs missing branch: %v", args)
	}
	if len(args) == 0 || args[0] != "clone" {
		t.Fatalf("CloneArgs should start with \"clone\", got %v", args)
	}
}

func TestCloneArgsNoToken(t *testing.T) {
	// [negative] CloneArgs never embeds a token, even if a token-shaped string is passed as URL by mistake.
	// The clone URL itself must never contain injected credentials from this builder.
	args := CloneArgs("https://github.com/owner/repo.git", "/tmp/work/repo", "build/4")
	for _, a := range args {
		if strings.Contains(a, "@github.com") && strings.Contains(a, ":") && strings.Contains(a, "ghp_") {
			t.Fatalf("CloneArgs embedded a credential-shaped URL: %v", args)
		}
	}
}

func TestCommitArgsPositive(t *testing.T) {
	// [positive] CommitArgs constructs correct git commit argv.
	args := CommitArgs("build 4: sync")
	joined := strings.Join(args, " ")
	if len(args) == 0 || args[0] != "commit" {
		t.Fatalf("CommitArgs should start with \"commit\", got %v", args)
	}
	if !strings.Contains(joined, "build 4: sync") {
		t.Fatalf("CommitArgs missing message: %v", args)
	}
}

func TestPushArgsPositive(t *testing.T) {
	// [positive] git command args for a push are constructed correctly.
	args := PushArgs("origin", "build/4")
	if len(args) == 0 || args[0] != "push" {
		t.Fatalf("PushArgs should start with \"push\", got %v", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "origin") || !strings.Contains(joined, "build/4") {
		t.Fatalf("PushArgs missing remote/branch: %v", args)
	}
}

func TestPushArgsNoToken(t *testing.T) {
	// [negative] PushArgs contains no token substring — token flows only via PushRequest.Token -> env.
	args := PushArgs("origin", "build/4")
	token := "ghp_supersecrettoken"
	for _, a := range args {
		if strings.Contains(a, token) {
			t.Fatalf("PushArgs leaked token: %v", args)
		}
		if strings.Contains(a, "ghp_") {
			t.Fatalf("PushArgs contains a token-shaped substring: %v", args)
		}
	}
}

// ---------- DecryptPAT ----------

func TestDecryptPATPositive(t *testing.T) {
	// [positive] DecryptPAT returns the plaintext PAT via secretbox round-trip.
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, err := secretbox.NewFromHex(hexKey)
	if err != nil {
		t.Fatalf("secretbox.NewFromHex failed: %v", err)
	}
	plainPAT := "ghp_thisisatestpat1234567890"
	encPAT, err := box.Encrypt([]byte(plainPAT))
	if err != nil {
		t.Fatalf("box.Encrypt failed: %v", err)
	}
	got, err := DecryptPAT(box, encPAT)
	if err != nil {
		t.Fatalf("DecryptPAT returned error: %v", err)
	}
	if got != plainPAT {
		t.Fatalf("DecryptPAT = %q, want %q", got, plainPAT)
	}
}

func TestDecryptPATBadCiphertext(t *testing.T) {
	// [negative] a bad/empty PAT ciphertext -> error.
	hexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	box, err := secretbox.NewFromHex(hexKey)
	if err != nil {
		t.Fatalf("secretbox.NewFromHex failed: %v", err)
	}
	_, err = DecryptPAT(box, []byte{})
	if err == nil {
		t.Fatal("DecryptPAT with empty ciphertext should error")
	}
}

func TestDecryptPATNilBox(t *testing.T) {
	// [negative] nil box -> error, not a panic.
	_, err := DecryptPAT(nil, []byte("whatever"))
	if err == nil {
		t.Fatal("DecryptPAT with nil box should error")
	}
}

func TestDecryptPATWrongKey(t *testing.T) {
	// [negative] decrypting with the wrong key -> error (bad PAT scenario).
	hexKey1 := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	hexKey2 := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	box1, _ := secretbox.NewFromHex(hexKey1)
	box2, _ := secretbox.NewFromHex(hexKey2)
	encPAT, err := box1.Encrypt([]byte("ghp_realpat"))
	if err != nil {
		t.Fatalf("box1.Encrypt failed: %v", err)
	}
	_, err = DecryptPAT(box2, encPAT)
	if err == nil {
		t.Fatal("DecryptPAT with wrong key should error")
	}
}

// ---------- GitCLI.Push arg construction / token hygiene (no actual exec) ----------

func TestGitCLIPushArgsTokenHygiene(t *testing.T) {
	// [negative] Building the push env/args for GitCLI must never place the token
	// in argv; it must flow via env only. We verify via the pure arg builders that
	// back GitCLI.Push, since we do not invoke the real git binary in tests.
	remote := "origin"
	branch := BranchName(4)
	args := PushArgs(remote, branch)
	token := "ghp_hygienecheck"
	for _, a := range args {
		if strings.Contains(a, token) {
			t.Fatalf("push args leaked token: %v", args)
		}
	}
}

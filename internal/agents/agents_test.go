package agents_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/secretbox"
	"github.com/giantbeaver9/yuno/internal/testutil"
	"github.com/jackc/pgx/v5"
)

// testBox returns a *secretbox.Box built from an all-zero test key. Fine for
// tests; production keys come from config.
func testBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.NewFromHex(strings.Repeat("0", 64))
	if err != nil {
		t.Fatalf("secretbox.NewFromHex: %v", err)
	}
	return box
}

func fullParams(name string) agents.CreateParams {
	return agents.CreateParams{
		Name:         name,
		Provider:     "openai",
		Model:        "gpt-5",
		RecipePath:   "recipes/coder.yaml",
		Prompt:       "you are a coder",
		Tools:        []string{"bash", "edit"},
		Mode:         "approval",
		MaxCost:      12.5,
		RateLimit:    30,
		BlockedTools: []string{"rm"},
		Guid:         "guid-1234",
		Roles:        []string{"coder"},
	}
}

// [positive] Create persists all config knobs and the one role; Get returns
// them intact.
func TestCreateAndGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	p := fullParams("coder-agent")
	created, err := s.Create(ctx, p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatalf("Create: expected non-zero ID")
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.ID != created.ID {
		t.Fatalf("Get.ID = %d, want %d", got.ID, created.ID)
	}
	if got.Name != p.Name {
		t.Fatalf("Get.Name = %q, want %q", got.Name, p.Name)
	}
	if got.Provider != p.Provider {
		t.Fatalf("Get.Provider = %q, want %q", got.Provider, p.Provider)
	}
	if got.Model != p.Model {
		t.Fatalf("Get.Model = %q, want %q", got.Model, p.Model)
	}
	if got.RecipePath != p.RecipePath {
		t.Fatalf("Get.RecipePath = %q, want %q", got.RecipePath, p.RecipePath)
	}
	if got.Prompt != p.Prompt {
		t.Fatalf("Get.Prompt = %q, want %q", got.Prompt, p.Prompt)
	}
	if !equalStrSlices(got.Tools, p.Tools) {
		t.Fatalf("Get.Tools = %v, want %v", got.Tools, p.Tools)
	}
	if got.Mode != p.Mode {
		t.Fatalf("Get.Mode = %q, want %q", got.Mode, p.Mode)
	}
	if got.MaxCost != p.MaxCost {
		t.Fatalf("Get.MaxCost = %v, want %v", got.MaxCost, p.MaxCost)
	}
	if got.RateLimit != p.RateLimit {
		t.Fatalf("Get.RateLimit = %d, want %d", got.RateLimit, p.RateLimit)
	}
	if !equalStrSlices(got.BlockedTools, p.BlockedTools) {
		t.Fatalf("Get.BlockedTools = %v, want %v", got.BlockedTools, p.BlockedTools)
	}
	if got.Guid != p.Guid {
		t.Fatalf("Get.Guid = %q, want %q", got.Guid, p.Guid)
	}
	if !equalStrSlices(got.Roles, []string{"coder"}) {
		t.Fatalf("Get.Roles = %v, want [coder]", got.Roles)
	}
}

// [positive] Mode defaults to "auto" when the caller leaves it empty.
func TestCreateDefaultsMode(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	p := agents.CreateParams{Name: "default-mode-agent"}
	created, err := s.Create(ctx, p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Mode != "auto" {
		t.Fatalf("Create.Mode = %q, want %q", created.Mode, "auto")
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Mode != "auto" {
		t.Fatalf("Get.Mode = %q, want %q", got.Mode, "auto")
	}
}

// [positive] SetProviderKey stores an ENCRYPTED value (raw bytes differ from
// plaintext) and GetProviderKey decrypts it back to the original.
func TestSetAndGetProviderKeyEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	created, err := s.Create(ctx, agents.CreateParams{Name: "key-agent"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const plaintext = "sk-x"
	if err := s.SetProviderKey(ctx, created.ID, "gemini", plaintext); err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}

	// Raw bytes in the DB must NOT equal the plaintext (proves encryption).
	var raw []byte
	err = st.Pool.QueryRow(ctx,
		"SELECT enc_key FROM provider_key WHERE agent_id = $1 AND provider = $2",
		created.ID, "gemini",
	).Scan(&raw)
	if err != nil {
		t.Fatalf("raw select enc_key: %v", err)
	}
	if string(raw) == plaintext {
		t.Fatalf("enc_key stored as plaintext bytes, want encrypted")
	}

	got, err := s.GetProviderKey(ctx, created.ID, "gemini")
	if err != nil {
		t.Fatalf("GetProviderKey: %v", err)
	}
	if got != plaintext {
		t.Fatalf("GetProviderKey = %q, want %q", got, plaintext)
	}
}

// [positive] Roles set via SetRoles come back through both Get and List.
func TestRolesViaGetAndList(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	created, err := s.Create(ctx, agents.CreateParams{Name: "role-agent"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.SetRoles(ctx, created.ID, []string{"coder"}); err != nil {
		t.Fatalf("SetRoles: %v", err)
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !equalStrSlices(got.Roles, []string{"coder"}) {
		t.Fatalf("Get.Roles = %v, want [coder]", got.Roles)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := findAgent(list, created.ID)
	if found == nil {
		t.Fatalf("List: agent %d not found", created.ID)
	}
	if !equalStrSlices(found.Roles, []string{"coder"}) {
		t.Fatalf("List.Roles = %v, want [coder]", found.Roles)
	}
}

// [positive] List returns all created agents, including config knobs, roles,
// and guid (the GET /agents shape).
func TestListReturnsCreatedAgents(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	a1, err := s.Create(ctx, fullParams("agent-one"))
	if err != nil {
		t.Fatalf("Create(agent-one): %v", err)
	}
	a2, err := s.Create(ctx, agents.CreateParams{Name: "agent-two", Guid: "guid-2", Roles: []string{"reviewer"}})
	if err != nil {
		t.Fatalf("Create(agent-two): %v", err)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List: got %d agents, want 2", len(list))
	}

	f1 := findAgent(list, a1.ID)
	if f1 == nil {
		t.Fatalf("List: agent-one not found")
	}
	if f1.Guid != "guid-1234" || !equalStrSlices(f1.Roles, []string{"coder"}) {
		t.Fatalf("List: agent-one = %+v", f1)
	}

	f2 := findAgent(list, a2.ID)
	if f2 == nil {
		t.Fatalf("List: agent-two not found")
	}
	if f2.Guid != "guid-2" || !equalStrSlices(f2.Roles, []string{"reviewer"}) {
		t.Fatalf("List: agent-two = %+v", f2)
	}
}

// [negative] Get on a nonexistent id returns a sensible not-found error.
func TestGetNonexistentID(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	_, err := s.Get(ctx, 999999)
	if err == nil {
		t.Fatalf("Get(999999): expected error, got nil")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Get(999999) err = %v, want wrapping pgx.ErrNoRows", err)
	}
}

// [negative] GetProviderKey when no key has been set returns an error, not a
// crash or a zero-value success.
func TestGetProviderKeyNoneSet(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	created, err := s.Create(ctx, agents.CreateParams{Name: "no-key-agent"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = s.GetProviderKey(ctx, created.ID, "gemini")
	if err == nil {
		t.Fatalf("GetProviderKey: expected error, got nil")
	}
}

// [edge] An agent with multiple roles: Create with several Roles, all come
// back via Get, sorted.
func TestCreateWithMultipleRoles(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	created, err := s.Create(ctx, agents.CreateParams{
		Name:  "multi-role-agent",
		Roles: []string{"reviewer", "coder", "deployer"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := []string{"coder", "deployer", "reviewer"} // sorted
	if !equalStrSlices(got.Roles, want) {
		t.Fatalf("Get.Roles = %v, want %v", got.Roles, want)
	}
}

// [edge] SetProviderKey called twice (different keys) upserts: one row
// remains and GetProviderKey returns the latest value.
func TestSetProviderKeyTwiceUpserts(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	created, err := s.Create(ctx, agents.CreateParams{Name: "upsert-agent"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.SetProviderKey(ctx, created.ID, "gemini", "sk-old"); err != nil {
		t.Fatalf("SetProviderKey(first): %v", err)
	}
	if err := s.SetProviderKey(ctx, created.ID, "gemini", "sk-new"); err != nil {
		t.Fatalf("SetProviderKey(second): %v", err)
	}

	var count int
	err = st.Pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM provider_key WHERE agent_id = $1 AND provider = $2",
		created.ID, "gemini",
	).Scan(&count)
	if err != nil {
		t.Fatalf("count provider_key rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("provider_key row count = %d, want 1", count)
	}

	got, err := s.GetProviderKey(ctx, created.ID, "gemini")
	if err != nil {
		t.Fatalf("GetProviderKey: %v", err)
	}
	if got != "sk-new" {
		t.Fatalf("GetProviderKey = %q, want %q", got, "sk-new")
	}
}

// [edge] Create with empty Roles produces no agent_roles rows, yet List still
// returns the agent.
func TestCreateWithEmptyRolesStillListed(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	created, err := s.Create(ctx, agents.CreateParams{Name: "no-role-agent"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var count int
	err = st.Pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM agent_roles WHERE agent_id = $1", created.ID,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count agent_roles: %v", err)
	}
	if count != 0 {
		t.Fatalf("agent_roles row count = %d, want 0", count)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := findAgent(list, created.ID)
	if found == nil {
		t.Fatalf("List: agent %d not found", created.ID)
	}
	if len(found.Roles) != 0 {
		t.Fatalf("List.Roles = %v, want empty", found.Roles)
	}
}

// [edge] SetRoles called twice with ["coder","coder"] leaves exactly one
// "coder" role row: dedup, and no PK violation on replace.
func TestSetRolesDedupReplace(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	s := agents.New(st.Pool, testBox(t))

	created, err := s.Create(ctx, agents.CreateParams{Name: "dedup-agent"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.SetRoles(ctx, created.ID, []string{"coder", "coder"}); err != nil {
		t.Fatalf("SetRoles(first): %v", err)
	}
	if err := s.SetRoles(ctx, created.ID, []string{"coder", "coder"}); err != nil {
		t.Fatalf("SetRoles(second): %v", err)
	}

	roles, err := s.GetRoles(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRoles: %v", err)
	}
	if !equalStrSlices(roles, []string{"coder"}) {
		t.Fatalf("GetRoles = %v, want [coder]", roles)
	}
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findAgent(list []agents.Agent, id int64) *agents.Agent {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

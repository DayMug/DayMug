package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// twoProviderCfg mirrors the wiring the HTTP tests use: a stock claude
// account plus a codex one, so the provider/model pairing rules have two
// families to get wrong.
func twoProviderCfg() *config.Config {
	return &config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
		{Name: "codex", Type: config.CLITypeCodex},
	}}
}

// svcStatusOf unwraps the ServiceError a use-case returned. Everything the
// transport layer renders comes from these two fields, so asserting on them
// is equivalent to asserting on the HTTP response.
func svcStatusOf(t *testing.T, err error) (int, string) {
	t.Helper()
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T: %v", err, err)
	}
	return svcErr.Status, svcErr.Message()
}

// ownedBy returns a fake holding just the conversation owner's row. Create
// reads it for work_dir / default-model seeding and refuses when the read
// fails, so every create fixture needs the owner to exist.
func ownedBy(userID string) *storetest.Fake {
	ms := storetest.New()
	ms.Users = []store.User{{ID: userID}}
	return ms
}

func TestConversationOps_Create_DefaultsToServerProvider(t *testing.T) {
	ms := ownedBy("u1")
	ops := &ConversationOps{Store: ms, Cfg: twoProviderCfg()}

	conv, err := ops.Create(context.Background(), "", CreateConversationParams{Title: "t", UserID: "u1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if conv.Provider != config.CLITypeClaude {
		t.Fatalf("expected provider %q, got %q", config.CLITypeClaude, conv.Provider)
	}
	if want := LatestModelForAccount(ops.Cfg, config.CLITypeClaude, ""); conv.Model != want {
		t.Fatalf("expected model %q, got %q", want, conv.Model)
	}
}

// A user's admin-set default model implies its provider, so seeding must
// resolve the pair together — otherwise a codex default would be written onto
// a claude conversation.
func TestConversationOps_Create_SeedsProviderFromUserDefaultModel(t *testing.T) {
	cfg := twoProviderCfg()
	codexModel := LatestModelForAccount(cfg, config.CLITypeCodex, "")
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", WorkDir: "/home/u1", DefaultModel: codexModel, ThinkLevel: "high"}}
	ops := &ConversationOps{Store: ms, Cfg: cfg}

	conv, err := ops.Create(context.Background(), "", CreateConversationParams{Title: "t", UserID: "u1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if conv.Provider != config.CLITypeCodex || conv.Model != codexModel {
		t.Fatalf("expected (%s, %s), got (%s, %s)", config.CLITypeCodex, codexModel, conv.Provider, conv.Model)
	}
	if conv.WorkDir != "/home/u1" {
		t.Fatalf("expected work_dir inherited from user, got %q", conv.WorkDir)
	}
	if conv.ThinkLevel != "high" {
		t.Fatalf("expected think_level inherited with the default model, got %q", conv.ThinkLevel)
	}
}

func TestConversationOps_Create_ExplicitProviderDefaultClearsAgentThinkLevel(t *testing.T) {
	cfg := twoProviderCfg()
	ms := storetest.New()
	ms.Users = []store.User{{
		ID: "u1", DefaultModel: LatestModelForAccount(cfg, config.CLITypeClaude, ""), ThinkLevel: "high",
	}}
	ops := &ConversationOps{Store: ms, Cfg: cfg}
	providerDefault := ""
	conv, err := ops.Create(context.Background(), "", CreateConversationParams{
		UserID: "u1", ThinkLevel: &providerDefault,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if conv.ThinkLevel != "" {
		t.Fatalf("think_level = %q, want provider default", conv.ThinkLevel)
	}
}

// An explicit provider must suppress the user's default-model seeding
// entirely: the seed only fires when the caller pinned neither half of the
// pair.
func TestConversationOps_Create_ExplicitProviderIgnoresUserDefaultModel(t *testing.T) {
	cfg := twoProviderCfg()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", DefaultModel: LatestModelForAccount(cfg, config.CLITypeCodex, "")}}
	ops := &ConversationOps{Store: ms, Cfg: cfg}

	conv, err := ops.Create(context.Background(), "", CreateConversationParams{UserID: "u1", Provider: config.CLITypeClaude})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if conv.Provider != config.CLITypeClaude {
		t.Fatalf("expected claude, got %q", conv.Provider)
	}
	if want := LatestModelForAccount(cfg, config.CLITypeClaude, ""); conv.Model != want {
		t.Fatalf("expected model %q, got %q", want, conv.Model)
	}
}

// A default model that no configured provider claims must be dropped rather
// than written through — it would otherwise pin the conversation to a backend
// that can't serve it.
func TestConversationOps_Create_UnknownUserDefaultModelFallsBack(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", DefaultModel: "model-from-a-removed-provider"}}
	ops := &ConversationOps{Store: ms, Cfg: twoProviderCfg()}

	conv, err := ops.Create(context.Background(), "", CreateConversationParams{UserID: "u1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if conv.Provider != config.CLITypeClaude {
		t.Fatalf("expected fallback to the first configured provider, got %q", conv.Provider)
	}
}

func TestConversationOps_Create_CrossFamilyModelRejected(t *testing.T) {
	cfg := twoProviderCfg()
	ops := &ConversationOps{Store: ownedBy("u1"), Cfg: cfg}

	_, err := ops.Create(context.Background(), "", CreateConversationParams{
		UserID:   "u1",
		Provider: config.CLITypeClaude,
		Model:    LatestModelForAccount(cfg, config.CLITypeCodex, ""),
	})
	status, msg := svcStatusOf(t, err)
	if status != http.StatusBadRequest || msg != "model does not belong to provider" {
		t.Fatalf("expected 400 model mismatch, got %d %q", status, msg)
	}
}

func TestConversationOps_Create_NoProviderConfigured(t *testing.T) {
	ops := &ConversationOps{Store: ownedBy("u1"), Cfg: &config.Config{}}

	_, err := ops.Create(context.Background(), "", CreateConversationParams{UserID: "u1"})
	status, msg := svcStatusOf(t, err)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", status, msg)
	}
	if msg != "no provider configured: add at least one entry to providers in config.yaml" {
		t.Fatalf("unexpected message %q", msg)
	}
}

func TestConversationOps_Create_AccountMustBeBoundToUser(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", ProviderAccounts: map[string][]string{config.CLITypeClaude: {"default"}}}}
	ops := &ConversationOps{Store: ms, Cfg: twoProviderCfg()}

	_, err := ops.Create(context.Background(), "", CreateConversationParams{
		UserID:   "u1",
		Provider: config.CLITypeClaude,
		Account:  "someone-elses",
	})
	status, msg := svcStatusOf(t, err)
	if status != http.StatusBadRequest || msg != "account not bound to this user for provider "+config.CLITypeClaude {
		t.Fatalf("expected 400 unbound account, got %d %q", status, msg)
	}
	if len(ms.Conversations) != 0 {
		t.Fatalf("conversation must not be created when the account check fails: %+v", ms.Conversations)
	}
}

func TestConversationOps_Create_RejectsBoundAccountRemovedFromConfig(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", ProviderAccounts: map[string][]string{config.CLITypeCodex: {"removed-codex"}}}}
	ops := &ConversationOps{Store: ms, Cfg: twoProviderCfg()}

	_, err := ops.Create(context.Background(), "", CreateConversationParams{
		UserID:   "u1",
		Provider: config.CLITypeCodex,
		Account:  "removed-codex",
	})
	status, msg := svcStatusOf(t, err)
	if status != http.StatusBadRequest || msg != "provider account is not configured for provider "+config.CLITypeCodex {
		t.Fatalf("expected 400 stale account, got %d %q", status, msg)
	}
	if len(ms.Conversations) != 0 {
		t.Fatalf("conversation must not be created for a removed provider account: %+v", ms.Conversations)
	}
}

func TestConversationOps_Create_BoundAccountIsPinned(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", ProviderAccounts: map[string][]string{config.CLITypeClaude: {"default"}}}}
	ops := &ConversationOps{Store: ms, Cfg: twoProviderCfg()}

	conv, err := ops.Create(context.Background(), "", CreateConversationParams{
		UserID:   "u1",
		Provider: config.CLITypeClaude,
		Account:  "default",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if conv.AccountName != "default" {
		t.Fatalf("expected account pin to survive the re-read, got %q", conv.AccountName)
	}
}

func TestConversationOps_Create_RequiresUserID(t *testing.T) {
	ops := &ConversationOps{Store: storetest.New(), Cfg: twoProviderCfg()}

	_, err := ops.Create(context.Background(), "", CreateConversationParams{Title: "t"})
	status, msg := svcStatusOf(t, err)
	if status != http.StatusBadRequest || msg != "user_id is required" {
		t.Fatalf("expected 400 user_id is required, got %d %q", status, msg)
	}
}

// startedConversation returns a fake pre-loaded with a conversation that has
// already taken its first user turn — the state that locks provider/account.
func startedConversation(t *testing.T, conv store.Conversation) *storetest.Fake {
	t.Helper()
	ms := storetest.New()
	ms.Conversations = []store.Conversation{conv}
	ms.Messages[conv.ID] = []store.Message{{ID: "m1", ConversationID: conv.ID, Role: "user", Content: "hi"}}
	return ms
}

func TestConversationOps_UpdateModel_ProviderLockedAfterFirstTurn(t *testing.T) {
	cfg := twoProviderCfg()
	ms := startedConversation(t, store.Conversation{ID: "c1", UserID: "u1", Provider: config.CLITypeClaude})
	ops := &ConversationOps{Store: ms, Cfg: cfg}

	_, err := ops.UpdateModel(context.Background(), "c1", "", config.CLITypeCodex, LatestModelForAccount(cfg, config.CLITypeCodex, ""), nil, nil)
	status, msg := svcStatusOf(t, err)
	if status != http.StatusConflict || msg != "cannot change provider after the conversation has started" {
		t.Fatalf("expected 409 provider locked, got %d %q", status, msg)
	}
	if ms.Conversations[0].Provider != config.CLITypeClaude {
		t.Fatalf("row must be untouched, got provider %q", ms.Conversations[0].Provider)
	}
}

// Re-pinning the account after the first turn is a separate 409 with its own
// wording: the conversation's history and rate limit live on the account it
// ran under, so it is bound there for good.
func TestConversationOps_UpdateModel_AccountLockedAfterFirstTurn(t *testing.T) {
	cfg := twoProviderCfg()
	ms := startedConversation(t, store.Conversation{ID: "c1", UserID: "u1", Provider: config.CLITypeClaude, AccountName: "default"})
	ms.Users = []store.User{{ID: "u1", ProviderAccounts: map[string][]string{config.CLITypeClaude: {"default", "other"}}}}
	ops := &ConversationOps{Store: ms, Cfg: cfg}

	other := "other"
	_, err := ops.UpdateModel(context.Background(), "c1", "", config.CLITypeClaude, LatestModelForAccount(cfg, config.CLITypeClaude, ""), &other, nil)
	status, msg := svcStatusOf(t, err)
	if status != http.StatusConflict || msg != "cannot change account after the conversation has started" {
		t.Fatalf("expected 409 account locked, got %d %q", status, msg)
	}
}

// Clearing the pin back to the default is still a change of account, so the
// same 409 applies — a started conversation cannot fall back to whatever the
// dispatcher would pick today.
func TestConversationOps_UpdateModel_ClearingAccountAfterFirstTurnIsConflict(t *testing.T) {
	cfg := twoProviderCfg()
	ms := startedConversation(t, store.Conversation{ID: "c1", UserID: "u1", Provider: config.CLITypeClaude, AccountName: "default"})
	ops := &ConversationOps{Store: ms, Cfg: cfg}

	empty := ""
	_, err := ops.UpdateModel(context.Background(), "c1", "", config.CLITypeClaude, LatestModelForAccount(cfg, config.CLITypeClaude, ""), &empty, nil)
	if status, _ := svcStatusOf(t, err); status != http.StatusConflict {
		t.Fatalf("expected 409, got %d", status)
	}
}

// The model itself stays mutable inside the locked provider — that is the
// whole point of the split gate.
func TestConversationOps_UpdateModel_ModelMutableAfterFirstTurn(t *testing.T) {
	cfg := twoProviderCfg()
	models := ModelsForAccount(cfg, config.CLITypeClaude, "")
	if len(models) < 2 {
		t.Skip("claude registry has no second model to switch to")
	}
	ms := startedConversation(t, store.Conversation{ID: "c1", UserID: "u1", Provider: config.CLITypeClaude, Model: models[0]})
	ops := &ConversationOps{Store: ms, Cfg: cfg}

	updated, err := ops.UpdateModel(context.Background(), "c1", "", config.CLITypeClaude, models[1], nil, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Model != models[1] {
		t.Fatalf("expected model %q, got %q", models[1], updated.Model)
	}
}

func TestConversationOps_UpdateModel_ThinkLevelMutableAfterFirstTurn(t *testing.T) {
	cfg := twoProviderCfg()
	model := LatestModelForAccount(cfg, config.CLITypeClaude, "")
	ms := startedConversation(t, store.Conversation{
		ID: "c1", UserID: "u1", Provider: config.CLITypeClaude, Model: model, ThinkLevel: "high",
	})
	ops := &ConversationOps{Store: ms, Cfg: cfg}
	level := " low "
	updated, err := ops.UpdateModel(context.Background(), "c1", "", config.CLITypeClaude, model, nil, &level)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ThinkLevel != "low" {
		t.Fatalf("think_level = %q, want low", updated.ThinkLevel)
	}
}

func TestConversationOps_UpdateModel_RejectsInvalidThinkLevel(t *testing.T) {
	cfg := twoProviderCfg()
	model := LatestModelForAccount(cfg, config.CLITypeClaude, "")
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Provider: config.CLITypeClaude, Model: model,
	}}
	ops := &ConversationOps{Store: ms, Cfg: cfg}
	level := "extreme"
	_, err := ops.UpdateModel(context.Background(), "c1", "", config.CLITypeClaude, model, nil, &level)
	status, _ := svcStatusOf(t, err)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", status)
	}
}

// Switching provider before the first turn is allowed, and must drop the old
// pin: even though account names are globally unique, the previous account is
// not configured for the new backend.
func TestConversationOps_UpdateModel_ProviderSwitchClearsStalePin(t *testing.T) {
	cfg := twoProviderCfg()
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Provider: config.CLITypeClaude, AccountName: "default"}}
	ops := &ConversationOps{Store: ms, Cfg: cfg}

	updated, err := ops.UpdateModel(context.Background(), "c1", "", config.CLITypeCodex, LatestModelForAccount(cfg, config.CLITypeCodex, ""), nil, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Provider != config.CLITypeCodex {
		t.Fatalf("expected codex, got %q", updated.Provider)
	}
	if updated.AccountName != "" {
		t.Fatalf("expected the stale account pin to be cleared, got %q", updated.AccountName)
	}
}

func TestConversationOps_UpdateModel_RejectsBoundAccountRemovedFromConfig(t *testing.T) {
	cfg := twoProviderCfg()
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Provider: config.CLITypeCodex, AccountName: "codex"}}
	ms.Users = []store.User{{ID: "u1", ProviderAccounts: map[string][]string{config.CLITypeCodex: {"codex", "removed-codex"}}}}
	ops := &ConversationOps{Store: ms, Cfg: cfg}

	removed := "removed-codex"
	_, err := ops.UpdateModel(context.Background(), "c1", "", config.CLITypeCodex, LatestModelForAccount(cfg, config.CLITypeCodex, ""), &removed, nil)
	status, msg := svcStatusOf(t, err)
	if status != http.StatusBadRequest || msg != "provider account is not configured for provider "+config.CLITypeCodex {
		t.Fatalf("expected 400 stale account, got %d %q", status, msg)
	}
	if got := ms.Conversations[0].AccountName; got != "codex" {
		t.Fatalf("account changed after rejected update: %q", got)
	}
}

func TestConversationOps_UpdateModel_RequiresProviderAndModel(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Provider: config.CLITypeClaude}}
	ops := &ConversationOps{Store: ms, Cfg: twoProviderCfg()}

	_, err := ops.UpdateModel(context.Background(), "c1", "", config.CLITypeClaude, "", nil, nil)
	status, msg := svcStatusOf(t, err)
	if status != http.StatusBadRequest || msg != "provider and model are required" {
		t.Fatalf("expected 400, got %d %q", status, msg)
	}
}

// A missing conversation must surface as 404 before any of the field
// validation runs, so a caller can't probe for row existence via 400s.
func TestConversationOps_UpdateModel_UnknownConversation(t *testing.T) {
	ops := &ConversationOps{Store: storetest.New(), Cfg: twoProviderCfg()}

	_, err := ops.UpdateModel(context.Background(), "nope", "", "", "", nil, nil)
	status, msg := svcStatusOf(t, err)
	if status != http.StatusNotFound || msg != "conversation not found" {
		t.Fatalf("expected 404 conversation not found, got %d %q", status, msg)
	}
}

func TestConversationOps_UpdateWorkDir_RejectsPathOutsideOwnerHome(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", WorkDir: "/home/alice"}}
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ops := &ConversationOps{Store: ms}

	// The classic prefix-match trap: /home/alice2 is a sibling, not a child.
	_, err := ops.UpdateWorkDir(context.Background(), "c1", "", "/home/alice2/proj")
	status, msg := svcStatusOf(t, err)
	if status != http.StatusForbidden || msg != "work_dir must be within the user's home" {
		t.Fatalf("expected 403, got %d %q", status, msg)
	}
}

// A symlink planted inside the owner's home that points outside it must not
// widen the jail. The containment test therefore has to dereference links,
// exactly like the agent spawn path and the file handlers do — otherwise the
// same boundary gets two different answers and a user can escape by creating
// a link they are perfectly entitled to create in their own home.
func TestConversationOps_UpdateWorkDir_RejectsSymlinkEscapingOwnerHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{home, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	escape := filepath.Join(home, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	inside := filepath.Join(home, "proj")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", WorkDir: home}}
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ops := &ConversationOps{Store: ms}

	_, err := ops.UpdateWorkDir(context.Background(), "c1", "", escape)
	status, msg := svcStatusOf(t, err)
	if status != http.StatusForbidden || msg != "work_dir must be within the user's home" {
		t.Fatalf("symlink out of home: expected 403, got %d %q", status, msg)
	}

	// Sanity check the other direction: resolving links must not reject a
	// genuinely contained directory.
	conv, err := ops.UpdateWorkDir(context.Background(), "c1", "", inside)
	if err != nil {
		t.Fatalf("real subdirectory of home rejected: %v", err)
	}
	if conv.WorkDir != inside {
		t.Fatalf("work_dir = %q, want %q", conv.WorkDir, inside)
	}
}

func TestConversationOps_UpdateWorkDir_LockedAfterFirstTurn(t *testing.T) {
	ms := startedConversation(t, store.Conversation{ID: "c1", UserID: "u1"})
	ms.Users = []store.User{{ID: "u1", WorkDir: "/home/alice"}}
	ops := &ConversationOps{Store: ms}

	_, err := ops.UpdateWorkDir(context.Background(), "c1", "", "/home/alice/proj")
	status, msg := svcStatusOf(t, err)
	if status != http.StatusConflict || msg != "working directory is locked" {
		t.Fatalf("expected 409, got %d %q", status, msg)
	}
}

// An owner row the store can't produce fails the create instead of being
// shrugged off. The tolerant version wrote a conversation with an empty
// work_dir — an unrecoverable, silent mis-rooting — while the account check on
// the same read already returned an error, so the two now agree.
func TestConversationOps_Create_UnreadableOwnerRowRefusesCreate(t *testing.T) {
	cfg := twoProviderCfg()
	claudeModel := LatestModelForAccount(cfg, config.CLITypeClaude, "")

	// Each case withholds a different field so a different consumer of the
	// owner row is the one that needs it.
	cases := []struct {
		name string
		p    CreateConversationParams
	}{
		{"work_dir seeding", CreateConversationParams{UserID: "ghost", Provider: config.CLITypeClaude, Model: claudeModel}},
		{"provider/model seeding", CreateConversationParams{UserID: "ghost", WorkDir: "/home/ghost"}},
		{"account pin check", CreateConversationParams{UserID: "ghost", WorkDir: "/home/ghost", Provider: config.CLITypeClaude, Model: claudeModel, Account: "default"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := storetest.New() // no user rows at all
			ops := &ConversationOps{Store: ms, Cfg: cfg}

			_, err := ops.Create(context.Background(), "", tc.p)
			status, msg := svcStatusOf(t, err)
			if status != http.StatusNotFound || msg != "user not found" {
				t.Fatalf("expected 404 user not found, got %d %q", status, msg)
			}
			if len(ms.Conversations) != 0 {
				t.Fatalf("no conversation may be persisted: %+v", ms.Conversations)
			}
		})
	}
}

// The read is only taken when a decision depends on it: a request that pins
// work_dir, both halves of the provider/model pair and no account needs
// nothing from the owner row, so it must not inherit the new failure mode.
func TestConversationOps_Create_FullySpecifiedRequestNeedsNoOwnerRow(t *testing.T) {
	cfg := twoProviderCfg()
	ms := storetest.New()
	ops := &ConversationOps{Store: ms, Cfg: cfg}

	conv, err := ops.Create(context.Background(), "", CreateConversationParams{
		UserID:   "ghost",
		WorkDir:  "/home/ghost",
		Provider: config.CLITypeClaude,
		Model:    LatestModelForAccount(cfg, config.CLITypeClaude, ""),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if conv.WorkDir != "/home/ghost" {
		t.Fatalf("work_dir = %q, want the caller-supplied path", conv.WorkDir)
	}
}

// The provider must be validated before the account, otherwise a typo in the
// provider name is reported as an account binding problem: bindings are keyed
// by provider, so an unknown key simply has none.
func TestConversationOps_UpdateModel_UnknownProviderBeatsAccountCheck(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Provider: config.CLITypeClaude}}
	ms.Users = []store.User{{ID: "u1", ProviderAccounts: map[string][]string{config.CLITypeClaude: {"default"}}}}
	ops := &ConversationOps{Store: ms, Cfg: twoProviderCfg()}

	account := "default"
	_, err := ops.UpdateModel(context.Background(), "c1", "", "clyde", "clyde-1", &account, nil)
	status, msg := svcStatusOf(t, err)
	if status != http.StatusBadRequest || msg != "unknown provider" {
		t.Fatalf("expected 400 unknown provider, got %d %q", status, msg)
	}
}

// Clearing history has to reach the chat body of the owner's other tabs. The
// sidebar row is unchanged, so the fan-out goes to the conversation room —
// `conversation_updated` on the user hub only re-splices sidebar rows and would
// leave the deleted messages on screen until a manual refresh.
func TestConversationOps_ClearMessages_BroadcastsToConversationRoom(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"}}
	bc := NewBroadcaster()
	peer := make(chan []byte, 4)
	bc.Join("c1", "peer-tab", peer)
	ops := &ConversationOps{Store: ms, Broadcaster: bc}

	if err := ops.ClearMessages(context.Background(), "c1", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}

	var frame ServerMessage
	select {
	case data := <-peer:
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("decode: %v", err)
		}
	default:
		t.Fatal("peer tab received no frame")
	}
	if frame.Type != "messages_cleared" || frame.ConversationID != "c1" {
		t.Fatalf("unexpected frame: %+v", frame)
	}

	// Must not have been buffered for replay: a tab joining later reloads the
	// (already empty) history from the store, and a buffered wipe could later
	// be replayed over messages sent after the clear.
	late := make(chan []byte, 4)
	bc.Join("c1", "late-tab", late)
	if len(late) != 0 {
		t.Fatalf("clear frame must stay out of the replay buffer, late joiner got %d frame(s)", len(late))
	}
}

// ResetSession is deliberately the one write path with no lifecycle
// broadcast — see its doc comment. Pinned here so the absence reads as a
// decision rather than an oversight: the row's visible fields don't change, and
// what peer tabs do care about (the token bar) is handled by dropping the
// cached usage frame.
func TestConversationOps_ResetSession_SendsNoLifecycleBroadcast(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice"}}
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", SessionID: "old"}}
	hub := NewUserHub()
	peer := make(chan []byte, 4)
	hub.Join("u1", "peer-tab", peer)
	bc := NewBroadcaster()
	bc.SetLastContextUsage("c1", []byte(`{"type":"context_usage","content":"{\"used\":42}"}`))
	ops := &ConversationOps{Store: ms, Broadcaster: bc, UserHub: hub}

	if _, err := ops.ResetSession(context.Background(), "c1", ""); err != nil {
		t.Fatalf("reset: %v", err)
	}

	if len(peer) != 0 {
		t.Fatalf("expected no user-hub event, got %d", len(peer))
	}
	if bc.HasContextUsage("c1") {
		t.Fatal("cached context_usage must be dropped so a joining tab isn't re-seeded with the pre-reset bar")
	}
}

// A nil Broadcaster is the documented test/degraded wiring: the busy guard
// simply never fires and the rotation still happens.
func TestConversationOps_ResetSession_NilBroadcasterRotates(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", SessionID: "old"}}
	ops := &ConversationOps{Store: ms}

	updated, err := ops.ResetSession(context.Background(), "c1", "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if updated.SessionID == "" || updated.SessionID == "old" {
		t.Fatalf("expected a rotated session id, got %q", updated.SessionID)
	}
}

// A nil UserHub must not panic on any write path — the lifecycle fan-out is
// optional wiring that only routes.go supplies.
func TestConversationOps_NilUserHubIsSafe(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ops := &ConversationOps{Store: ms, Cfg: twoProviderCfg()}

	if _, err := ops.UpdateTitle(context.Background(), "c1", "", "renamed"); err != nil {
		t.Fatalf("update title: %v", err)
	}
	if err := ops.Delete(context.Background(), "c1", ""); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// A compatible provider ships no built-in model catalog, so an account that
// an admin added but has not yet given models to resolves to an empty model.
// The generic "model does not belong to provider" message points the caller
// at their request when the actual fix is an admin filling in the registry,
// so this path has to name that instead.
func TestConversationOps_Create_CompatibleProviderWithNoModelsSaysSo(t *testing.T) {
	ms := ownedBy("u1")
	ops := &ConversationOps{Store: ms, Cfg: &config.Config{Providers: []config.Provider{
		{Name: "glm", Type: config.CLITypeClaudeCompatible},
	}}}

	_, err := ops.Create(context.Background(), "", CreateConversationParams{
		Title: "t", UserID: "u1", Provider: config.CLITypeClaudeCompatible,
	})
	status, msg := svcStatusOf(t, err)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if msg != NoModelsConfiguredError(config.CLITypeClaudeCompatible) {
		t.Errorf("message = %q, want the empty-registry explanation", msg)
	}
}

// With models configured the same provider creates normally — proving the
// branch above is about the registry being empty, not about the type.
func TestConversationOps_Create_CompatibleProviderUsesRegistryModel(t *testing.T) {
	ms := ownedBy("u1")
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "glm", Type: config.CLITypeClaudeCompatible},
	}}
	withAccountModels(t, map[string]AccountModels{
		"glm": {Models: []string{"glm-5", "glm-5-air"}},
	})
	ops := &ConversationOps{Store: ms, Cfg: cfg}

	conv, err := ops.Create(context.Background(), "", CreateConversationParams{
		Title: "t", UserID: "u1", Provider: config.CLITypeClaudeCompatible,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if conv.Model != "glm-5" {
		t.Errorf("model = %q, want the registry's first entry", conv.Model)
	}
}

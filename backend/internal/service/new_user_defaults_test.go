package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestNewUserDefaultsRoundTripAndValidation(t *testing.T) {
	ctx := context.Background()
	ms := storetest.New()
	cfg := twoProviderCfg()
	withAccountModels(t, map[string]AccountModels{
		"codex": {Models: []string{"custom-model"}},
	})
	legacy, err := LoadNewUserDefaults(ctx, ms, cfg)
	if err != nil || legacy.ProviderName != "default" {
		t.Fatalf("legacy defaults = %+v, %v", legacy, err)
	}
	want := NewUserDefaults{ProviderType: "codex", ProviderName: "codex", Model: "custom-model"}
	if _, err := SaveNewUserDefaults(ctx, ms, cfg, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadNewUserDefaults(ctx, ms, cfg)
	if err != nil || got != want {
		t.Fatalf("loaded = %+v, %v; want %+v", got, err, want)
	}
	for _, invalid := range []NewUserDefaults{
		{ProviderType: "codex", ProviderName: "missing"},
		{ProviderType: "claude", ProviderName: "codex"},
		{ProviderType: "codex", ProviderName: "codex", Model: "unknown-model"},
		{Model: "custom-model"},
	} {
		if _, err := SaveNewUserDefaults(ctx, ms, cfg, invalid); err == nil {
			t.Fatalf("accepted invalid defaults: %+v", invalid)
		}
		if got, err := LoadNewUserDefaults(ctx, ms, cfg); err != nil || got != want {
			t.Fatalf("invalid save changed defaults: %+v, %v", got, err)
		}
	}
	if _, err := SaveNewUserDefaults(ctx, ms, cfg, NewUserDefaults{}); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadNewUserDefaults(ctx, ms, cfg); err != nil || got != (NewUserDefaults{}) {
		t.Fatalf("explicit unbound must survive a reload: %+v, %v", got, err)
	}
}

func TestNewUserDefaultsProvisioning(t *testing.T) {
	for _, entry := range []string{"admin", "oidc"} {
		for _, mode := range []string{"model", "account-default", "unbound", "removed-account", "removed-model", "explicit-empty", "explicit-account"} {
			t.Run(entry+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				db, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				if err := db.Init(); err != nil {
					t.Fatal(err)
				}
				cfg := &config.Config{
					Users: config.UsersConfig{DefaultHomeRoot: t.TempDir()},
					Providers: []config.Provider{
						{Name: "default", Type: config.CLITypeClaude},
						{Name: "special", Type: config.CLITypeClaude},
					},
				}
				withAccountModels(t, map[string]AccountModels{
					"default": {Models: []string{"first-model"}},
					"special": {Models: []string{"custom-model", "chosen-model"}},
				})
				d := NewUserDefaults{ProviderType: "claude", ProviderName: "special", Model: "chosen-model"}
				wantAccount, wantModel := "special", "chosen-model"
				if mode == "unbound" {
					d = NewUserDefaults{}
					wantAccount, wantModel = "", ""
				}
				if mode == "account-default" {
					d.Model = ""
					wantModel = "custom-model"
				}
				if _, err := SaveNewUserDefaults(ctx, db, cfg, d); err != nil {
					t.Fatal(err)
				}
				if mode == "removed-account" {
					cfg.Providers = cfg.Providers[:1]
					wantAccount, wantModel = "", ""
				}
				if mode == "removed-model" {
					SetModelOverrides(map[string]AccountModels{"special": {Models: []string{"other-model"}}})
					wantAccount, wantModel = "", ""
				}
				var user store.User
				if entry == "admin" {
					params := AdminCreateUserParams{Email: "alice@example.com", Password: "password"}
					if mode == "explicit-empty" {
						params.ProviderBindings = map[string]string{}
						wantAccount, wantModel = "", ""
					}
					if mode == "explicit-account" {
						params.ProviderAccounts = map[string][]string{"claude": {"default"}}
						wantAccount, wantModel = "default", ""
					}
					user, err = (&AdminUserOps{Store: db, Cfg: cfg}).CreateHuman(ctx, params)
				} else {
					user, err = (&OIDCUserOps{Store: db, Cfg: cfg}).ProvisionUser(ctx, &OIDCIdentity{Email: "alice@example.com"})
				}
				if err != nil {
					t.Fatal(err)
				}
				persisted, err := db.GetUser(ctx, user.ID)
				if err != nil {
					t.Fatal(err)
				}
				if persisted.ProviderBindings["claude"] != wantAccount || persisted.DefaultModel != wantModel {
					t.Fatalf("persisted defaults = %v, %q; want %q, %q", persisted.ProviderBindings, persisted.DefaultModel, wantAccount, wantModel)
				}
				users, err := db.ListUsers(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, agent := range users {
					if agent.OwnerID != user.ID {
						continue
					}
					if agent.DefaultModel != wantModel || agent.ProviderBindings["claude"] != wantAccount {
						t.Fatalf("first Agent did not inherit defaults: %+v", agent)
					}
					if wantModel != "" {
						seed, err := ResolveConversationSeed(cfg, agent, ConversationSeedRequest{})
						if err != nil || seed.Model != wantModel {
							t.Fatalf("first chat seed: %+v, %v", seed, err)
						}
					}
				}
				// Changing the policy must not update a previously created user.
				if _, err := SaveNewUserDefaults(ctx, db, cfg, NewUserDefaults{}); err != nil {
					t.Fatal(err)
				}
				unchanged, err := db.GetUser(ctx, user.ID)
				if err != nil || unchanged.DefaultModel != wantModel || unchanged.ProviderBindings["claude"] != wantAccount {
					t.Fatalf("policy change touched an existing user: %+v, %v", unchanged, err)
				}
			})
		}
	}
}

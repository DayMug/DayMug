package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

const NewUserDefaultsKey = "users.defaults"

// NewUserDefaults grants one account and optionally pins its model for future
// humans. All-empty explicitly means no account; it is persisted as JSON so it
// remains distinct from an installation that has never configured this setting.
type NewUserDefaults struct {
	ProviderType string `json:"provider_type"`
	ProviderName string `json:"provider_name"`
	Model        string `json:"model"`
}

func LoadNewUserDefaults(ctx context.Context, s AppSettingStore, cfg *config.Config) (NewUserDefaults, error) {
	raw, err := s.GetAppSetting(ctx, NewUserDefaultsKey)
	if err != nil {
		return NewUserDefaults{}, Internal("load new user defaults", err)
	}
	if raw == "" {
		// Preserve existing installations' initial account until an admin saves.
		if p, ok := cfg.InitialBindingProvider(); ok {
			return NewUserDefaults{ProviderType: p.Type, ProviderName: p.Name}, nil
		}
		return NewUserDefaults{}, nil
	}
	var defaults NewUserDefaults
	if err := json.Unmarshal([]byte(raw), &defaults); err != nil {
		return NewUserDefaults{}, Internal("invalid new user defaults", err)
	}
	return defaults, nil
}

func (d NewUserDefaults) validate(cfg *config.Config) error {
	if d == (NewUserDefaults{}) {
		return nil
	}
	if cfg == nil || d.ProviderName == "" || cfg.FindAccountForType(d.ProviderName, d.ProviderType) == nil {
		return BadRequest("unknown provider account for new users")
	}
	if d.Model != "" && !IsValidModelForAccount(cfg, d.ProviderType, d.ProviderName, d.Model) {
		return BadRequest("model does not belong to the new user provider account")
	}
	return nil
}

func SaveNewUserDefaults(ctx context.Context, s AppSettingStore, cfg *config.Config, d NewUserDefaults) (NewUserDefaults, error) {
	d.ProviderType = strings.TrimSpace(d.ProviderType)
	d.ProviderName = strings.TrimSpace(d.ProviderName)
	d.Model = strings.TrimSpace(d.Model)
	if err := d.validate(cfg); err != nil {
		return NewUserDefaults{}, err
	}
	buf, err := json.Marshal(d)
	if err != nil {
		return NewUserDefaults{}, Internal("encode new user defaults", err)
	}
	if err := s.SetAppSetting(ctx, NewUserDefaultsKey, string(buf)); err != nil {
		return NewUserDefaults{}, Internal("save new user defaults", err)
	}
	return d, nil
}

// ApplyNewUserDefaults runs only before inserting a new human. Removed accounts
// or models leave newcomers unbound; they must never grant a different account.
func ApplyNewUserDefaults(ctx context.Context, s AppSettingStore, cfg *config.Config, user *store.User) error {
	d, err := LoadNewUserDefaults(ctx, s, cfg)
	if err != nil {
		return err
	}
	if d.ProviderName == "" || d.validate(cfg) != nil {
		return nil
	}
	user.ProviderBindings = map[string]string{d.ProviderType: d.ProviderName}
	user.DefaultModel = d.Model
	if user.DefaultModel == "" {
		user.DefaultModel = LatestModelForAccount(cfg, d.ProviderType, d.ProviderName)
	}
	return nil
}

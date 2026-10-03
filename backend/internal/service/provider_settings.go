package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// ProviderSettingsKey stores the complete credential-pool registry. Keeping
// it in one app_settings row makes an admin reorder/add/delete atomic and lets
// the first row remain the default provider for new conversations.
const ProviderSettingsKey = "providers.accounts"

// LoadProviderSettings hydrates the runtime cache from SQLite. A missing row
// and a stored [] both leave the installation with no runnable providers
// until an admin configures one in the UI.
func LoadProviderSettings(ctx context.Context, s AppSettingStore, cfg *config.Config) error {
	if s == nil || cfg == nil {
		return nil
	}
	raw, err := s.GetAppSetting(ctx, ProviderSettingsKey)
	if err != nil {
		return fmt.Errorf("load providers: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return cfg.ReplaceProviders(nil)
	}
	var providers []config.Provider
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		return fmt.Errorf("parse providers: %w", err)
	}
	if err := cfg.ReplaceProviders(providers); err != nil {
		return fmt.Errorf("validate providers: %w", err)
	}
	return nil
}

// SaveProviderSettings validates, persists, then hot-swaps the provider
// registry. The runtime cache only changes after SQLite accepts the write.
func SaveProviderSettings(ctx context.Context, s AppSettingStore, cfg *config.Config, providers []config.Provider) error {
	if s == nil || cfg == nil {
		return Internal("provider setting store unavailable", nil)
	}
	normalized, err := config.NormalizeProviders(providers)
	if err != nil {
		return BadRequest(err.Error())
	}
	buf, err := json.Marshal(normalized)
	if err != nil {
		return Internal(err.Error(), err)
	}
	if err := s.SetAppSetting(ctx, ProviderSettingsKey, string(buf)); err != nil {
		return Internal(err.Error(), err)
	}
	if err := cfg.ReplaceProviders(normalized); err != nil {
		return Internal(err.Error(), err)
	}
	return nil
}
